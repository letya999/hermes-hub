package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
)

// CompactRequest is the /v1/compact body: the same trusted envelope the run
// endpoints carry plus the parsed upstream argument vocabulary. The session
// is always derived from the envelope — a caller can never name another
// conversation's transcript.
type CompactRequest struct {
	ExecuteRequest
	Here       int    `json:"here,omitempty"`
	Focus      string `json:"focus,omitempty"`
	Prune      bool   `json:"prune,omitempty"`
	Preview    bool   `json:"preview,omitempty"`
	Engine     string `json:"engine,omitempty"`
	BusyPolicy string `json:"busy_policy,omitempty"`
}

// compactHolder owns the manual path's lease and compression lock — the
// same writer identity the journal reads back.
const compactHolder = "hub-compact"

// compactSummarize is the production summarizer for the manual path; a
// package variable so tests can replace the relay call.
var compactSummarize = compaction.RelaySummarizeFocus

// compactBound mirrors upstream's hard ceiling on `here N`.
const compactBound = 100

func (s *runtimeHTTP) compact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	defer r.Body.Close()
	var request CompactRequest
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid compact request")
		return
	}
	check := request.ExecuteRequest
	check.Text = "compact"
	if err := validateExecuteRequest(check); err != nil {
		writeRuntimeError(w, http.StatusConflict, err.Error())
		return
	}
	if request.Here < 0 || request.Here > compactBound || len(request.Focus) > 512 ||
		(request.BusyPolicy != "" && request.BusyPolicy != "refuse" && request.BusyPolicy != "queue") {
		writeRuntimeError(w, http.StatusBadRequest, "invalid compact args")
		return
	}
	sessionID := sessionIDFor(request.ExecuteRequest)
	stateDir := env("HUB_STATE", "/state")
	mc := compaction.ManagedContextFromEnv()
	engine, granted := mc.CompactEngineFor(request.Engine)
	if !granted {
		result := compaction.CompactResult{FailureClass: "engine_denied"}
		if request.Engine == compaction.EngineHub || request.Engine == compaction.EngineNative {
			if journal, err := compaction.Open(stateDir); err == nil {
				_ = journal.Append(compaction.Event{At: time.Now().UTC(), SessionID: sessionID,
					Trigger: compaction.TriggerManual, Engine: request.Engine,
					Method: compaction.MethodSummary, FailureClass: "engine_denied"})
			}
		}
		writeRuntimeJSON(w, http.StatusOK, result)
		return
	}
	model := mc.SummaryModel
	if model == "" {
		model = os.Getenv("HUB_MANAGED_MODEL_ID")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, err := compaction.Compact(ctx, compaction.CompactOptions{
		StateDir: stateDir, SessionID: sessionID, Holder: compactHolder,
		Trigger: compaction.TriggerManual, Engine: engine, ResolvedModel: model,
		Threshold: mc.SweepGate().Threshold, TailCount: sweepTailCount,
		Args: compaction.CompactArgs{
			Here: request.Here, Focus: request.Focus,
			Prune: request.Prune, Preview: request.Preview,
		},
		Now:       time.Now().UTC,
		Summarize: compactSummarize,
	})
	if err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "compact failed")
		return
	}
	if result.FailureClass == compaction.RefusalLeaseHeld && request.BusyPolicy == "queue" {
		if err := compaction.QueueCompact(stateDir, sessionID, compaction.PendingCompact{
			Args:   compaction.CompactArgs{Here: request.Here, Focus: request.Focus, Prune: request.Prune, Preview: request.Preview},
			Engine: engine, At: time.Now().UTC(),
		}); err == nil {
			result.FailureClass = "queued"
		}
	}
	writeRuntimeJSON(w, http.StatusOK, result)
}
