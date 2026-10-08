package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// UsageRequest resolves measured session usage on the runtime that owns the
// envelope's session. Resident runtimes ignore the envelope; the supervisor
// routes on it exactly like /v1/artifact.
type UsageRequest struct {
	ExecuteRequest
}

// SessionUsage is the measured usage view for one durable Hermes session.
// Pointer fields stay nil — and serialize as null — whenever the pinned
// upstream API carries no authoritative value; consumers must render null as
// "unknown" and never estimate from transcript text or billed totals.
type SessionUsage struct {
	Object            string    `json:"object"`
	DeclaredSessionID string    `json:"declared_session_id"`
	SessionID         string    `json:"session_id"`
	SessionFound      bool      `json:"session_found"`
	Source            string    `json:"source"`
	FetchedAt         time.Time `json:"fetched_at"`
	Model             string    `json:"model,omitempty"`
	Title             string    `json:"title,omitempty"`
	StartedAt         string    `json:"started_at,omitempty"`
	InputTokens       *int64    `json:"input_tokens"`
	OutputTokens      *int64    `json:"output_tokens"`
	CacheReadTokens   *int64    `json:"cache_read_tokens"`
	CacheWriteTokens  *int64    `json:"cache_write_tokens"`
	ReasoningTokens   *int64    `json:"reasoning_tokens"`
	APICallCount      *int64    `json:"api_call_count"`
	MessageCount      *int64    `json:"message_count"`
	ToolCallCount     *int64    `json:"tool_call_count"`
	EstimatedCostUSD  *float64  `json:"estimated_cost_usd"`
	ActualCostUSD     *float64  `json:"actual_cost_usd"`
	// Current-prompt context and the model window are distinct from cumulative
	// billing counters. The pinned API exposes neither, so they stay null.
	ContextTokens *int64 `json:"context_tokens"`
	ContextWindow *int64 `json:"context_window"`
	// Compactions counts measured session rotations along the durable
	// parent_session_id lineage; LastCompactionAt is the retired parent's
	// ended_at. In-place compactions leave no row and are not fabricated.
	Compactions      *int64 `json:"compactions"`
	LastCompactionAt string `json:"last_compaction_at,omitempty"`
	// CompactionsTruncated marks the lineage walk stopped at the hop cap with
	// more ancestors present; consumers render Compactions as a lower bound
	// (≥N) instead of an exact count.
	CompactionsTruncated bool   `json:"compactions_truncated,omitempty"`
	LastActive           string `json:"last_active,omitempty"`
}

const usageSourceHermesSessionDB = "hermes_session_db"

// usageMaxLineageHops bounds the sequential parent walk: each hop is one
// HTTP call and a deep lineage kept /usage busy for minutes.
const usageMaxLineageHops = 4

func (s *runtimeHTTP) usage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	var request UsageRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Envelope.ConversationID == "" || request.Envelope.ContextID == "" {
		writeRuntimeError(w, http.StatusBadRequest, "invalid usage request")
		return
	}
	report, err := s.measureUsage(r.Context(), request)
	if err != nil {
		writeRuntimeError(w, http.StatusBadGateway, "usage measurement unavailable")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, report)
}

// measureUsage resolves the effective session id through the upstream
// resume-resolution surface, then copies only fields the pinned
// /api/sessions/{id} response actually carries.
func (s *runtimeHTTP) measureUsage(ctx context.Context, request UsageRequest) (SessionUsage, error) {
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	auth := env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH"))
	client := &http.Client{Timeout: 10 * time.Second}
	declared := sessionIDFor(request.ExecuteRequest)
	report := SessionUsage{
		Object:            "hub.session_usage",
		DeclaredSessionID: declared,
		SessionID:         declared,
		Source:            usageSourceHermesSessionDB,
		FetchedAt:         time.Now().UTC(),
	}
	var page struct {
		SessionID string `json:"session_id"`
	}
	if err := hermesRequest(ctx, client, http.MethodGet, base+"/api/sessions/"+url.PathEscape(declared)+"/messages?limit=0", auth, nil, &page); err != nil && !usageSessionMissing(err) {
		return report, err
	}
	if strings.TrimSpace(page.SessionID) != "" {
		report.SessionID = page.SessionID
	}
	row, found, err := usageSessionRow(ctx, client, base, auth, report.SessionID)
	if err != nil {
		return report, err
	}
	if !found {
		return report, nil
	}
	report.SessionFound = true
	usageFillSession(&report, row)
	usageFillLineage(ctx, client, base, auth, &report, row)
	return report, nil
}

func usageSessionMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "404")
}

// usageSessionRow reads one /api/sessions/{id} row; found=false on 404.
func usageSessionRow(ctx context.Context, client *http.Client, base, auth, id string) (map[string]any, bool, error) {
	var envelope struct {
		Session map[string]any `json:"session"`
	}
	if err := hermesRequest(ctx, client, http.MethodGet, base+"/api/sessions/"+url.PathEscape(id), auth, nil, &envelope); err != nil {
		if usageSessionMissing(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if envelope.Session == nil {
		return nil, false, nil
	}
	return envelope.Session, true, nil
}

func usageFillSession(report *SessionUsage, row map[string]any) {
	report.Model = usageString(row, "model")
	report.Title = usageString(row, "title")
	report.StartedAt = usageString(row, "started_at")
	report.InputTokens = usageInt(row, "input_tokens")
	report.OutputTokens = usageInt(row, "output_tokens")
	report.CacheReadTokens = usageInt(row, "cache_read_tokens")
	report.CacheWriteTokens = usageInt(row, "cache_write_tokens")
	report.ReasoningTokens = usageInt(row, "reasoning_tokens")
	report.APICallCount = usageInt(row, "api_call_count")
	report.MessageCount = usageInt(row, "message_count")
	report.ToolCallCount = usageInt(row, "tool_call_count")
	report.EstimatedCostUSD = usageFloat(row, "estimated_cost_usd")
	report.ActualCostUSD = usageFloat(row, "actual_cost_usd")
	report.LastActive = usageString(row, "last_active")
}

// usageFillLineage walks the durable parent chain: each hop is one measured
// session rotation. The immediate parent's ended_at is the last rotation time.
func usageFillLineage(ctx context.Context, client *http.Client, base, auth string, report *SessionUsage, row map[string]any) {
	var hops int64
	parent := usageString(row, "parent_session_id")
	seen := map[string]bool{report.SessionID: true}
	for parent != "" && !seen[parent] && hops < usageMaxLineageHops {
		seen[parent] = true
		prow, found, err := usageSessionRow(ctx, client, base, auth, parent)
		if err != nil || !found {
			break
		}
		hops++
		if report.LastCompactionAt == "" {
			report.LastCompactionAt = usageString(prow, "ended_at")
		}
		parent = usageString(prow, "parent_session_id")
	}
	report.CompactionsTruncated = parent != "" && !seen[parent]
	report.Compactions = &hops
}

func usageString(row map[string]any, key string) string {
	if v, ok := row[key].(string); ok {
		return v
	}
	return ""
}

// usageInt copies a numeric field only when upstream carried a non-negative
// number; null, strings and negatives stay unknown.
func usageInt(row map[string]any, key string) *int64 {
	v, ok := row[key].(float64)
	if !ok || v < 0 {
		return nil
	}
	n := int64(v)
	return &n
}

func usageFloat(row map[string]any, key string) *float64 {
	v, ok := row[key].(float64)
	if !ok || v < 0 {
		return nil
	}
	return &v
}
