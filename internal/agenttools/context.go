package agenttools

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
)

// ContextStatus is the agent-facing bounded context view (SPEC-0072 R7):
// effective threshold with provenance, journaled compaction count and last
// event, and the live transcript estimate — each serialized as null when the
// substrate cannot measure it. The id argument selects a session; absent, the
// newest journaled session answers, since the agent cannot name its upstream
// session id.
func (t *Tools) ContextStatus(ctx context.Context, r Input) (any, error) {
	// A context argument naming anything but this runtime's bound context is
	// a cross-context probe — refuse before touching the journal.
	if r.Context != "" && r.Context != contextIdentity() {
		return nil, errors.New("context_status is bound to this runtime's context")
	}
	j, err := compaction.Open(t.StateDir)
	if err != nil {
		return nil, err
	}
	sessionID := r.ID
	if sessionID == "" {
		if sessionID, err = j.LatestSession(); err != nil {
			return nil, err
		}
	}
	threshold, declared := compaction.ManagedContextFromEnv().SweepThreshold()
	var tokens *int64
	if sessionID != "" {
		if n, _, err := compaction.EstimateTokens(ctx, sessionID); err == nil && n >= 0 {
			value := n
			tokens = &value
		}
	}
	st, err := j.StatusFor(sessionID, threshold, declared, tokens, compaction.ProvenanceEstimated)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"object":               "hub.context_status",
		"session_id":           sessionID,
		"threshold_tokens":     st.ThresholdTokens,
		"threshold_provenance": st.ThresholdProvenance,
		"compactions":          st.Compactions,
		"last_event":           st.LastEvent,
		"tokens":               st.Tokens,
		"tokens_provenance":    st.TokensProvenance,
	}, nil
}

// contextIdentity mirrors the runtime envelope's context binding.
func contextIdentity() string {
	if id := os.Getenv("HUB_CONTEXT_ID"); id != "" {
		return id
	}
	return os.Getenv("HUB_USER_ID")
}

// contextCompactSummarize is the tool path's summarizer seam — the same
// managed relay the channel command resolves, replaceable in tests.
var contextCompactSummarize = compaction.RelaySummarizeFocus

// ContextCompact is the agent-facing manual compaction surface (SPEC-0073
// R12): a write-effect call on this runtime's own session scope, grant-checked
// and journaled exactly like the channel command. The tool only ever returns
// the allowlisted result projection.
func (t *Tools) ContextCompact(ctx context.Context, r Input) (any, error) {
	if r.Context != "" && r.Context != contextIdentity() {
		return nil, errors.New("context_compact is bound to this runtime's context")
	}
	mc := compaction.ManagedContextFromEnv()
	engine, granted := mc.CompactEngineFor(r.Engine)
	sessionID := r.ID
	if !granted {
		if j, err := compaction.Open(t.StateDir); err == nil {
			_ = j.Append(compaction.Event{At: time.Now().UTC(), SessionID: sessionID,
				Trigger: compaction.TriggerManual, Engine: r.Engine,
				Method: compaction.MethodSummary, FailureClass: "engine_denied"})
		}
		return map[string]any{
			"object":        "hub.context_compact",
			"applied":       false,
			"failure_class": "engine_denied",
		}, nil
	}
	if sessionID == "" {
		// The in-run caller verifiably holds the turn lease; resolving its
		// session binds the tool to the caller's own conversation.
		if active, err := compaction.ActiveSession(ctx); err == nil {
			sessionID = active
		}
	}
	model := mc.SummaryModel
	if model == "" {
		model = os.Getenv("HUB_MANAGED_MODEL_ID")
	}
	result, err := compaction.Compact(ctx, compaction.CompactOptions{
		StateDir: t.StateDir, SessionID: sessionID, Holder: "hub-compact",
		Trigger: compaction.TriggerManual, Engine: engine,
		ResolvedModel: model,
		Args: compaction.CompactArgs{
			Here: r.Here, Focus: r.Focus, Prune: r.Prune, Preview: r.Preview,
		},
		Now:       time.Now().UTC,
		Summarize: contextCompactSummarize,
		Threshold: mc.SweepGate().Threshold, TailCount: 8,
	})
	if err != nil {
		return nil, err
	}
	if result.FailureClass == compaction.RefusalLeaseHeld && r.Queue {
		if err := compaction.QueueCompact(t.StateDir, sessionID, compaction.PendingCompact{
			Args:   compaction.CompactArgs{Here: r.Here, Focus: r.Focus, Prune: r.Prune, Preview: r.Preview},
			Engine: engine, At: time.Now().UTC(),
		}); err == nil {
			result.FailureClass = "queued"
		}
	}
	return map[string]any{
		"object":          "hub.context_compact",
		"session_id":      sessionID,
		"applied":         result.Applied,
		"method":          result.Method,
		"failure_class":   result.FailureClass,
		"messages_before": result.MessagesBefore,
		"messages_after":  result.MessagesAfter,
	}, nil
}
