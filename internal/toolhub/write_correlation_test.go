package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A lost response after a possible provider mutation replays the same logical
// write: the stable correlation digest fences the duplicate before any second
// effect, while ToolCallID still identifies each attempt.
func TestWriteCorrelationFencesReplayAfterLostResponse(t *testing.T) {
	store, auth, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record := privateRunRecord(auth, "job-1", "run-1")
	var calls atomic.Int32
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		if calls.Add(1) == 1 {
			return api.BackendResult{}, errors.New("fixture response lost after effect")
		}
		return api.BackendResult{Text: "sent"}, nil
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "send")
	call := func(callID string, arguments map[string]any) error {
		ctx := api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: callID})
		_, err := g.call(ctx, auth, name, arguments)
		return err
	}
	body := map[string]any{"message": "ship the release"}
	if err := call("call-1", body); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("lost response=%v", err)
	}
	if err := call("call-2", body); !errors.Is(err, audit.ErrUncertain) || calls.Load() != 1 {
		t.Fatalf("duplicate write reached backend: calls=%d err=%v", calls.Load(), err)
	}
	events, err := ledger.List(auth.PrincipalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || !strings.HasPrefix(events[0].CorrelationID, "call-") || events[0].CorrelationID != events[1].CorrelationID {
		t.Fatalf("write correlation not durable: %+v", events)
	}
	// Changed arguments are a different operation and pass the fence.
	if err := call("call-3", map[string]any{"message": "hold the release"}); err != nil || calls.Load() != 2 {
		t.Fatalf("distinct write fenced: calls=%d err=%v", calls.Load(), err)
	}
	// A settled logical operation is repeatable: the same arguments after a
	// proven completion start a fresh operation and reach the backend.
	if err := call("call-4", map[string]any{"message": "hold the release"}); err != nil || calls.Load() != 3 {
		t.Fatalf("settled operation not repeatable: calls=%d err=%v", calls.Load(), err)
	}
	// Operator resolves the uncertain write on evidence; the retry then
	// executes once because the fence releases settled correlations.
	if err := ledger.Resolve(audit.Scope{PrincipalID: auth.PrincipalID}, events[0].OperationID, "succeeded", "provider-ticket-482"); err != nil {
		t.Fatal(err)
	}
	if err := call("call-5", body); err != nil || calls.Load() != 4 {
		t.Fatalf("post-resolution retry: calls=%d err=%v", calls.Load(), err)
	}
}

// Concurrent identical writes admit exactly one effect; the others fence on the
// unsettled durable correlation.
func TestWriteCorrelationParallelIdenticalCalls(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	record := privateRunRecord(auth, "job-1", "run-1")
	var calls atomic.Int32
	start := make(chan struct{})
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls.Add(1)
		<-start
		return api.BackendResult{}, errors.New("fixture backend failure")
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "send")
	var wg sync.WaitGroup
	var uncertain atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: "call-" + string(rune('a'+i))})
			if _, err := g.call(ctx, auth, name, map[string]any{"message": "same"}); errors.Is(err, audit.ErrUncertain) {
				uncertain.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 || uncertain.Load() != 6 {
		t.Fatalf("parallel identical writes: backend=%d uncertain=%d", calls.Load(), uncertain.Load())
	}
}

// The correlation fence survives a process restart: a reopened ledger still
// recognizes the unsettled logical write.
func TestWriteCorrelationSurvivesLedgerReopen(t *testing.T) {
	store, auth, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record := privateRunRecord(auth, "job-1", "run-1")
	var calls atomic.Int32
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls.Add(1)
		return api.BackendResult{}, errors.New("fixture backend failure")
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "send")
	body := map[string]any{"message": "restart-proof"}
	ctx := api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: "call-1"})
	if _, err := g.call(ctx, auth, name, body); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	recovered, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g.AuditWrite = LedgerAuditWriter(recovered)
	ctx = api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: "call-2"})
	if _, err := g.call(ctx, auth, name, body); !errors.Is(err, audit.ErrUncertain) || calls.Load() != 1 {
		t.Fatalf("post-restart replay: calls=%d err=%v", calls.Load(), err)
	}
}

// Read tools keep per-attempt correlation: a read retry is never fenced.
func TestReadCorrelationStaysPerAttempt(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	record := privateRunRecord(auth, "job-1", "run-1")
	var calls atomic.Int32
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) {
		return record, nil
	}), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		calls.Add(1)
		return api.BackendResult{}, errors.New("fixture backend failure")
	})}
	name := api.ProjectedToolName("google-work", "1.0.0", "search")
	for _, callID := range []string{"call-1", "call-2"} {
		ctx := api.WithCallCorrelation(context.Background(), api.CallCorrelation{JobID: "job-1", HermesRunID: "run-1", ToolCallID: callID})
		if _, err := g.call(ctx, auth, name, nil); !errors.Is(err, audit.ErrUncertain) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("read retry fenced: calls=%d", calls.Load())
	}
}

// A known provider result is never masked by teardown failure: the caller
// keeps the result and the ledger records cleanup-error — not uncertain.
func TestCleanupErrorPreservesProviderResult(t *testing.T) {
	store, auth, _ := seededStore(t)
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: store, AuditWrite: LedgerAuditWriter(ledger), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: "sent", CleanupError: "remote-mcp-close"}, nil
	})}
	result, err := g.call(context.Background(), auth, api.ProjectedToolName("google-work", "1.0.0", "send"), map[string]any{"message": "x"})
	if err != nil || result == nil {
		t.Fatalf("cleanup failure lost provider result: %+v %v", result, err)
	}
	events, err := ledger.List(auth.PrincipalID)
	if err != nil || len(events) != 2 || events[1].Phase != "completed" || events[1].Execution != "succeeded" || events[1].Outcome != "cleanup-error" {
		t.Fatalf("cleanup outcome not durable: %+v %v", events, err)
	}
	if pending, err := ledger.Pending(audit.Scope{PrincipalID: auth.PrincipalID}, 1); err != nil || len(pending) != 0 {
		t.Fatalf("cleanup error left unsettled intent: %+v %v", pending, err)
	}
}
