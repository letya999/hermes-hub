package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Decode the on-disk contract so the old reader's missing lifecycle semantics
// produce a behavioral regression, rather than just a missing Go field error.
func lifecycleFields(t *testing.T, event Event, action, parent string) Event {
	t.Helper()
	body, err := json.Marshal(map[string]string{"cleanup_action": action, "cleanup_of": parent})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func lifecycleOpen(t *testing.T) Event {
	e := admittedEvent()
	e.Kind, e.Action, e.CorrelationID = "ssh", "ssh.shell_open", "shell-target"
	return lifecycleFields(t, e, "ssh.shell_close", "")
}

func lifecycleClose(t *testing.T, opened Event) Event {
	e := opened
	e.EventID = NewEvent("ssh", "alice", "allow").EventID
	e.OperationID, e.Action = "op-close", "ssh.shell_close"
	return lifecycleFields(t, e, "", opened.OperationID)
}

func TestLifecycleCleanupReserveSurvivesCompletionAndReopen(t *testing.T) {
	ledger, path := operationLedger(t)
	ledger.bounds = ledgerBounds{32 << 10, 128 << 10, 4, 1000}
	event := lifecycleOpen(t)
	event.JobID = strings.Repeat("j", 12*1024)
	opened, err := ledger.Begin(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Complete("succeeded", "not_applicable", "open", ""); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other.bounds = ledger.bounds
	accepted := 0
	for i := 0; i < 200; i++ {
		e := NewEvent("job", "bob", "allow")
		e.JobID = strings.Repeat("x", 1024)
		err := other.Append(e)
		if errors.Is(err, ErrFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted == 0 || accepted == 200 {
		t.Fatalf("filler records=%d", accepted)
	}
	cleanup, err := other.Begin(lifecycleClose(t, event))
	if err != nil {
		t.Fatalf("cleanup admission lost its lifecycle reserve: %v", err)
	}
	if err := cleanup.Complete("succeeded", "not_applicable", "close", ""); err != nil {
		t.Fatalf("cleanup completion lost its lifecycle reserve: %v", err)
	}
	pending, err := other.Pending(Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after verified close=%+v %v", pending, err)
	}
}

func TestLifecycleCleanupRequiresExactParentAndScope(t *testing.T) {
	for _, mutate := range []struct {
		name   string
		change func(*Event)
	}{
		{"principal", func(e *Event) { e.PrincipalID = "bob" }},
		{"context", func(e *Event) { e.ContextID = "foreign" }},
		{"environment", func(e *Event) { e.Environment = "prod" }},
		{"generation", func(e *Event) { e.Generation++ }},
		{"action", func(e *Event) { e.Action = "ssh.exec" }},
		{"missing-parent", func(e *Event) { *e = lifecycleFields(t, *e, "", "missing") }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			ledger, _ := operationLedger(t)
			e := lifecycleOpen(t)
			op, err := ledger.Begin(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := op.Complete("succeeded", "not_applicable", "open", ""); err != nil {
				t.Fatal(err)
			}
			closeEvent := lifecycleClose(t, e)
			mutate.change(&closeEvent)
			if _, err := ledger.Begin(closeEvent); !errors.Is(err, ErrInvalid) {
				t.Fatalf("foreign/unbound cleanup admitted: %v", err)
			}
		})
	}
}

func TestVerifiedClosePreservesUnknownOpenWithoutBlockingNewResource(t *testing.T) {
	ledger, path := operationLedger(t)
	e := lifecycleOpen(t)
	if _, err := ledger.Begin(e); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := recovered.Begin(lifecycleClose(t, e))
	if err != nil {
		t.Fatalf("cleanup of unsettled open rejected: %v", err)
	}
	if err := cleanup.Complete("succeeded", "not_applicable", "close", ""); err != nil {
		t.Fatal(err)
	}
	pending, err := recovered.Pending(Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 1 || pending[0].Execution != "unknown" || pending[0].OperationID != e.OperationID {
		t.Fatalf("open outcome fabricated: %+v %v", pending, err)
	}
	next := e
	next.EventID = NewEvent("ssh", "alice", "allow").EventID
	next.OperationID = "op-next"
	if _, err := recovered.Begin(next); err != nil {
		t.Fatalf("verified closed target stayed fenced: %v", err)
	}
	if _, err := recovered.Begin(e); !errors.Is(err, ErrUncertain) {
		t.Fatalf("same operation replayed: %v", err)
	}
	closeAgain := lifecycleClose(t, e)
	closeAgain.OperationID = "op-close-again"
	if _, err := recovered.Begin(closeAgain); !errors.Is(err, ErrUncertain) {
		t.Fatalf("cleanup replayed: %v", err)
	}
}

func TestAuditReceiptRejectsSensitivePayload(t *testing.T) {
	for _, value := range []string{"https://auth.invalid/?code=fixture", "https:fixture", "Bearer fixture-secret", `{"text":"fixture transcript"}`, "[fixture payload]", "access_token=fixture"} {
		ledger, _ := operationLedger(t)
		event := NewEvent("tool-call", "alice", "allow")
		event.Receipt = value
		if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
			t.Fatal("sensitive receipt accepted")
		}
	}
}

func TestUnverifiedCleanupNeverReleasesFence(t *testing.T) {
	ledger, path := operationLedger(t)
	e := lifecycleOpen(t)
	op, err := ledger.Begin(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Complete("succeeded", "not_applicable", "open", ""); err != nil {
		t.Fatal(err)
	}
	cleanup, err := op.BeginCleanup()
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Complete("failed", "not_applicable", "error", ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("unverified cleanup completed: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending(Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 1 || pending[0].CleanupOf != e.OperationID || pending[0].Execution != "unknown" {
		t.Fatalf("lost unresolved cleanup: %+v %v", pending, err)
	}
	e.OperationID = "op-new"
	if _, err := reopened.Begin(e); !errors.Is(err, ErrUncertain) {
		t.Fatalf("unverified resource was reopened: %v", err)
	}
}

func TestLifecycleMetadataValidation(t *testing.T) {
	ledger, _ := operationLedger(t)
	for _, change := range []func(*Event){
		func(e *Event) { e.CleanupAction = strings.Repeat("x", 129) },
		func(e *Event) { e.CleanupOf = "bad/parent" },
		func(e *Event) { e.CleanupOf = "parent" },
		func(e *Event) { e.Phase = "decision"; e.Execution = "not_started"; e.Delivery = "not_applicable" },
		func(e *Event) { e.Phase = "" },
		func(e *Event) { e.Schema = 1 },
	} {
		e := lifecycleOpen(t)
		e.Phase, e.Authorization, e.Execution, e.Delivery, e.Outcome = "admitted", "allow", "unknown", "unknown", "admitted"
		change(&e)
		if err := ledger.Append(e); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid lifecycle metadata accepted")
		}
	}
	var missing *Operation
	if _, err := missing.BeginCleanup(); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil opening supplied a cleanup reserve")
	}
	plain, err := ledger.Begin(admittedEvent())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.BeginCleanup(); !errors.Is(err, ErrInvalid) {
		t.Fatal("ordinary call supplied a cleanup reserve")
	}
}
