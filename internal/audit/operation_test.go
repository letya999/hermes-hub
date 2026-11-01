package audit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func operationLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return l, path
}
func admittedEvent() Event {
	e := NewEvent("tool-call", "alice", "allow")
	e.Action = "tool.call"
	e.Name = "tool-1"
	e.OperationID = "op-1"
	e.ContextID = "selected"
	e.Environment = "dev"
	return e
}

func TestCompletionReserveSurvivesHighWater(t *testing.T) {
	ledger, path := operationLedger(t)
	ledger.bounds = ledgerBounds{32 << 10, 64 << 10, 2, 100}
	event := admittedEvent()
	// Completion must be larger than the filler remainder, or success alone
	// would not prove that the reservation protected it.
	event.JobID = strings.Repeat("j", 12*1024)
	op, err := ledger.Begin(event)
	if err != nil {
		t.Fatal(err)
	}
	// A separately opened writer must recover the reservation from durable data.
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other.bounds = ledger.bounds
	accepted := 0
	for i := 0; i < 100; i++ {
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
	if accepted == 0 || accepted == 100 {
		t.Fatalf("capacity=%d", accepted)
	}
	if err := op.Complete("succeeded", "not_started", "ok", ""); err != nil {
		t.Fatalf("reserved completion lost: %v", err)
	}
	pending, err := other.Pending(Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("settlement=%+v %v", pending, err)
	}
}

func TestUnsettledAdmissionSurvivesReopenAndRotation(t *testing.T) {
	ledger, path := operationLedger(t)
	ledger.bounds = ledgerBounds{32 << 10, 128 << 10, 4, 1000}
	if _, err := ledger.Begin(admittedEvent()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		e := NewEvent("job", "bob", "allow")
		e.JobID = strings.Repeat("x", 1024)
		if err := ledger.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".v1-000001"); err != nil {
		t.Fatal("no rotation")
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	recovered.bounds = ledger.bounds
	pending, err := recovered.Pending(Scope{PrincipalID: "alice", ContextID: "selected", Environment: "dev"}, 10)
	if err != nil || len(pending) != 1 || pending[0].OperationID != "op-1" || pending[0].Execution != "unknown" || pending[0].Authorization != "allow" {
		t.Fatalf("recovery=%+v %v", pending, err)
	}
	if _, err := recovered.Begin(admittedEvent()); !errors.Is(err, ErrUncertain) {
		t.Fatalf("duplicate operation admitted: %v", err)
	}
	foreign, err := recovered.Pending(Scope{PrincipalID: "bob"}, 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("scope leak=%+v %v", foreign, err)
	}
}

func TestCompletionFailureKeepsDurableIntentAndCannotRetry(t *testing.T) {
	ledger, path := operationLedger(t)
	op, err := ledger.Begin(admittedEvent())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := op.Complete("succeeded", "not_started", "ok", ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("post effect=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if err := op.Complete("succeeded", "not_started", "ok", ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("completion repeated: %v", err)
	}
	pending, err := ledger.Pending(Scope{PrincipalID: "alice"}, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("intent lost: %+v %v", pending, err)
	}
}

func TestCompletionCannotChangeAuthorizationIdentity(t *testing.T) {
	ledger, _ := operationLedger(t)
	op, err := ledger.Begin(admittedEvent())
	if err != nil {
		t.Fatal(err)
	}
	bad := op.event
	bad.Phase = "completed"
	bad.Execution = "succeeded"
	bad.Outcome = "ok"
	bad.EventID = "evt-other"
	bad.PrincipalID = "bob"
	if err := ledger.Append(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign settlement=%v", err)
	}
	if err := op.Complete("failed", "not_started", "backend-error", ""); err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 2 || events[1].Authorization != "allow" || events[1].Execution != "failed" {
		t.Fatalf("decision conflated: %+v %v", events, err)
	}
	if err := op.Complete("succeeded", "succeeded", "ok", ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("settled twice: %v", err)
	}
}

func TestAdmissionCapacityFailureHasNoIntent(t *testing.T) {
	ledger, _ := operationLedger(t)
	ledger.bounds.total = 1024
	if _, err := ledger.Begin(admittedEvent()); !errors.Is(err, ErrFull) {
		t.Fatalf("unreserved admission: %v", err)
	}
	pending, err := ledger.Pending(Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("failed admission persisted: %+v %v", pending, err)
	}
}

func TestOperationContractRejectsMalformedClaims(t *testing.T) {
	for _, field := range []string{"schema", "phase", "authorization", "execution", "delivery", "id", "action", "outcome", "receipt", "incomplete"} {
		t.Run(field, func(t *testing.T) {
			ledger, _ := operationLedger(t)
			event := admittedEvent()
			event.Phase, event.Authorization, event.Execution, event.Delivery, event.Outcome = "admitted", "allow", "unknown", "unknown", "admitted"
			switch field {
			case "schema":
				event.Schema = 1
			case "phase":
				event.Phase = "new-version"
			case "authorization":
				event.Authorization = "deny"
			case "execution":
				event.Execution = "ok"
			case "delivery":
				event.Delivery = "ok"
			case "id":
				event.OperationID = "bad id"
			case "action":
				event.Action = "private prose"
			case "outcome":
				event.Outcome = "succeeded"
			case "receipt":
				event.Receipt = "effect claimed before admission"
			case "incomplete":
				event.Phase = ""
			}
			if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %s: %v", field, err)
			}
		})
	}
}

func TestAuthorizationDecisionIsSeparateFromExecution(t *testing.T) {
	ledger, _ := operationLedger(t)
	event := admittedEvent()
	event.Phase = "decision"
	event.Authorization = "deny"
	event.Execution = "not_started"
	event.Delivery = "not_applicable"
	event.Outcome = "denied"
	if err := ledger.Append(event); err != nil {
		t.Fatal(err)
	}
	pending, err := ledger.Pending(Scope{PrincipalID: "alice"}, 1)
	if err != nil || len(pending) != 0 {
		t.Fatalf("denial reserved effect: %+v %v", pending, err)
	}
	event.Execution = "succeeded"
	if err := ledger.Append(event); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unauthorized effect claim: %v", err)
	}
}

func TestPendingTargetFenceUsesDurableScope(t *testing.T) {
	ledger, path := operationLedger(t)
	event := admittedEvent()
	event.CorrelationID = "target-1"
	op, err := ledger.Begin(event)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	next := event
	next.EventID = "evt-next"
	next.OperationID = "op-2"
	if _, err := other.Begin(next); !errors.Is(err, ErrUncertain) {
		t.Fatalf("pending target repeated: %v", err)
	}
	foreign := next
	foreign.OperationID = "op-bob"
	foreign.PrincipalID = "bob"
	if _, err := other.Begin(foreign); err != nil {
		t.Fatalf("foreign owner inherited fence: %v", err)
	}
	if err := op.Complete("succeeded", "not_applicable", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Begin(next); err != nil {
		t.Fatalf("settled target remained blocked: %v", err)
	}
}

func TestUncertainObservationKeepsReserveAndScope(t *testing.T) {
	ledger, _ := operationLedger(t)
	event := admittedEvent()
	event.JobID = strings.Repeat("j", 12*1024)
	ledger.bounds = ledgerBounds{32 << 10, 64 << 10, 2, 100}
	op, err := ledger.Begin(event)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := op.event
	uncertain.EventID = "evt-uncertain"
	uncertain.Phase = "uncertain"
	uncertain.Outcome = "backend-error"
	if err := ledger.Append(uncertain); err != nil {
		t.Fatal(err)
	}
	pending, err := ledger.Pending(Scope{PrincipalID: "alice"}, 1)
	if err != nil || len(pending) != 1 || pending[0].Phase != "uncertain" {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	for i := 0; i < 100; i++ {
		e := NewEvent("job", "bob", "allow")
		e.JobID = strings.Repeat("x", 1024)
		err := ledger.Append(e)
		if errors.Is(err, ErrFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := op.Complete("succeeded", "not_started", "ok", ""); err != nil {
		t.Fatalf("uncertain observation spent reserve: %v", err)
	}
}
