package runtime

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// usageFixture fakes the pinned Hermes session API: a declared session that
// rotated once into an effective child row, plus rows with missing fields.
func usageFixture(t *testing.T, rows map[string]map[string]any, resolved map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/messages")
			resolvedID, ok := resolved[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "session_id": resolvedID, "data": []any{}, "pagination": map[string]any{"returned": 0}})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		row, ok := rows[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "hermes.session", "session": row})
	}))
}

func usageRequest(t *testing.T) UsageRequest {
	return UsageRequest{ExecuteRequest: validExecuteRequest("alice", "personal", "usage", "unused")}
}

func usageSetEnv(t *testing.T, api *httptest.Server) {
	t.Helper()
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	t.Setenv("API_SERVER_KEY", "secret")
	host, port, err := net.SplitHostPort(api.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
}

func TestUsageReportsMeasuredFieldsAndLineage(t *testing.T) {
	request := usageRequest(t)
	declared := sessionIDFor(request.ExecuteRequest)
	api := usageFixture(t, map[string]map[string]any{
		"eff": {
			"id": "eff", "model": "model-a", "input_tokens": 100.0, "output_tokens": 50.0,
			"cache_read_tokens": 10.0, "cache_write_tokens": 5.0, "reasoning_tokens": 7.0,
			"api_call_count": 3.0, "message_count": 8.0, "tool_call_count": 4.0,
			"estimated_cost_usd": 0.01, "actual_cost_usd": 0.02,
			"last_active": "2025-01-02T00:00:00Z", "parent_session_id": "parent-1",
		},
		"parent-1": {"id": "parent-1", "ended_at": "2025-01-01T00:00:00Z", "parent_session_id": "grand"},
		"grand":    {"id": "grand", "ended_at": "2024-12-31T00:00:00Z"},
	}, map[string]string{declared: "eff"})
	defer api.Close()
	usageSetEnv(t, api)

	req := httptest.NewRequest(http.MethodPost, "/v1/usage", strings.NewReader(mustJSON(t, request)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var report SessionUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.DeclaredSessionID != declared || report.SessionID != "eff" {
		t.Fatalf("effective session id lost: %+v", report)
	}
	if !report.SessionFound || report.Model != "model-a" || report.Source != usageSourceHermesSessionDB {
		t.Fatalf("report=%+v", report)
	}
	if report.InputTokens == nil || *report.InputTokens != 100 || report.OutputTokens == nil || *report.OutputTokens != 50 {
		t.Fatalf("cumulative tokens wrong: %+v", report)
	}
	if report.APICallCount == nil || *report.APICallCount != 3 || report.MessageCount == nil || *report.MessageCount != 8 {
		t.Fatalf("counts wrong: %+v", report)
	}
	if report.Compactions == nil || *report.Compactions != 2 || report.LastCompactionAt != "2025-01-01T00:00:00Z" {
		t.Fatalf("lineage wrong: %+v", report)
	}
	// The pinned API carries no authoritative prompt-context figure: it must
	// stay null, never an estimate from cumulative billing.
	if report.ContextTokens != nil || report.ContextWindow != nil {
		t.Fatalf("context fabricated: %+v", report)
	}
}

func TestUsageUnknownWhenUpstreamFieldsMissing(t *testing.T) {
	request := usageRequest(t)
	declared := sessionIDFor(request.ExecuteRequest)
	api := usageFixture(t, map[string]map[string]any{
		declared: {"id": declared, "model": "model-b", "input_tokens": "not-a-number", "output_tokens": -5.0},
	}, map[string]string{declared: declared})
	defer api.Close()
	usageSetEnv(t, api)

	report, err := (&runtimeHTTP{}).measureUsage(httptest.NewRequest(http.MethodGet, "/", nil).Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !report.SessionFound || report.InputTokens != nil || report.OutputTokens != nil || report.APICallCount != nil {
		t.Fatalf("missing/inconsistent fields must stay unknown: %+v", report)
	}
	if report.Compactions == nil || *report.Compactions != 0 {
		t.Fatalf("lineage count wrong: %+v", report)
	}
}

func TestUsageSessionNotCreatedYet(t *testing.T) {
	request := usageRequest(t)
	api := usageFixture(t, map[string]map[string]any{}, map[string]string{})
	defer api.Close()
	usageSetEnv(t, api)
	report, err := (&runtimeHTTP{}).measureUsage(httptest.NewRequest(http.MethodGet, "/", nil).Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionFound || report.SessionID != report.DeclaredSessionID {
		t.Fatalf("ghost session: %+v", report)
	}
}

func TestUsageSessionLookupErrorAndEmptyRow(t *testing.T) {
	request := usageRequest(t)
	declared := sessionIDFor(request.ExecuteRequest)
	measure := func() (SessionUsage, error) {
		return (&runtimeHTTP{}).measureUsage(httptest.NewRequest(http.MethodGet, "/", nil).Context(), request)
	}
	messagesReply := func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "session_id": declared, "data": []any{}, "pagination": map[string]any{"returned": 0}})
	}

	// A non-404 session lookup failure propagates; no half-read report.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			messagesReply(w)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	usageSetEnv(t, broken)
	if _, err := measure(); err == nil {
		t.Fatal("upstream 500 produced a usage report")
	}
	broken.Close()

	// An empty session envelope means "not found", not an error.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			messagesReply(w)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "hermes.session"})
	}))
	usageSetEnv(t, empty)
	report, err := measure()
	if err != nil || report.SessionFound {
		t.Fatalf("empty session row: %+v err=%v", report, err)
	}
	empty.Close()

	// A dangling parent pointer ends the lineage walk without failing the
	// report: the measured row still stands on its own.
	dangling := usageFixture(t, map[string]map[string]any{
		"eff": {"id": "eff", "model": "model-a", "parent_session_id": "gone"},
	}, map[string]string{declared: "eff"})
	defer dangling.Close()
	usageSetEnv(t, dangling)
	report, err = measure()
	if err != nil || !report.SessionFound || report.Model != "model-a" {
		t.Fatalf("dangling parent: %+v err=%v", report, err)
	}
	if report.Compactions == nil || *report.Compactions != 0 {
		t.Fatalf("dangling lineage counted: %+v", report.Compactions)
	}
}

func TestUsageRejectsUnauthorized(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	req := httptest.NewRequest(http.MethodPost, "/v1/usage", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", rec.Code)
	}
}

func TestUsageRejectsMalformedAndUnreachable(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	call := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/usage", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := call(http.MethodGet, "{}"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("method=%d", rec.Code)
	}
	if rec := call(http.MethodPost, "{not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("json=%d", rec.Code)
	}
	if rec := call(http.MethodPost, `{"object":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("envelope=%d", rec.Code)
	}
	// A well-formed request against an unreachable Hermes surfaces 502, never
	// a fabricated report.
	t.Setenv("HUB_HERMES_API_HOST", "127.0.0.1")
	t.Setenv("HUB_HERMES_API_PORT", "1")
	if rec := call(http.MethodPost, mustJSON(t, usageRequest(t))); rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable=%d body=%s", rec.Code, rec.Body.String())
	}
}
