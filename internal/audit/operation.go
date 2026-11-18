package audit

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

var ErrUncertain = errors.New("execution outcome unknown; do not repeat operation; reconcile audit")

func validateOperation(e Event) error {
	if e.Phase == "" {
		if e.OperationID != "" || e.Action != "" || e.Authorization != "" || e.Execution != "" || e.Delivery != "" || e.CleanupAction != "" || e.CleanupOf != "" {
			return fmt.Errorf("%w: incomplete operation fields", ErrInvalid)
		}
		return nil
	}
	if e.Schema != Schema || e.OperationID == "" || e.Action == "" || (e.Authorization != "allow" && e.Authorization != "deny") || e.Phase != "admitted" && e.Phase != "completed" && e.Phase != "uncertain" && e.Phase != "decision" {
		return fmt.Errorf("%w: operation contract", ErrInvalid)
	}
	for _, value := range []string{e.OperationID, e.Action, e.Outcome} {
		if !operationCode(value) {
			return fmt.Errorf("%w: operation code", ErrInvalid)
		}
	}

	if e.CleanupAction != "" && (!operationCode(e.CleanupAction) || e.CleanupOf != "" || e.Phase == "decision") || e.CleanupOf != "" && (!operationCode(e.CleanupOf) || e.Phase == "decision") {
		return fmt.Errorf("%w: lifecycle contract", ErrInvalid)
	}
	if e.CleanupOf != "" && e.Phase == "completed" && (e.Execution != "succeeded" || e.Delivery != "not_applicable") {
		return fmt.Errorf("%w: cleanup requires verified closure", ErrInvalid)
	}
	switch e.Execution {
	case "unknown", "not_started", "succeeded", "failed":
	default:
		return fmt.Errorf("%w: execution outcome", ErrInvalid)
	}
	switch e.Delivery {
	case "unknown", "not_started", "succeeded", "failed", "not_applicable":
	default:
		return fmt.Errorf("%w: delivery outcome", ErrInvalid)
	}
	if e.Phase == "decision" {
		if e.Execution != "not_started" || e.Delivery != "not_applicable" {
			return fmt.Errorf("%w: decision outcome", ErrInvalid)
		}
		return nil
	}
	if e.Authorization != "allow" {
		return fmt.Errorf("%w: execution without authorization", ErrInvalid)
	}
	if e.Phase == "uncertain" && (e.Execution != "unknown" || e.Delivery != "unknown" || e.Receipt != "") {
		return fmt.Errorf("%w: uncertain outcome", ErrInvalid)
	}
	if e.Phase == "completed" && e.Execution == "unknown" {
		return fmt.Errorf("%w: unknown completion", ErrInvalid)
	}
	if e.Phase == "admitted" && (e.Execution != "unknown" || e.Delivery != "unknown" || e.Outcome != "admitted" || e.Receipt != "") {
		return fmt.Errorf("%w: admission outcome", ErrInvalid)
	}
	return nil
}

func operationCode(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func sameOperation(admitted, completed Event) bool {
	admitted.EventID = completed.EventID
	admitted.At = completed.At
	admitted.Phase = completed.Phase
	admitted.Execution = completed.Execution
	admitted.Delivery = completed.Delivery
	admitted.Outcome = completed.Outcome
	admitted.Receipt = completed.Receipt
	return admitted == completed
}

// Operation owns one durable admission and at most one completion attempt.
type Operation struct {
	mu        sync.Mutex
	ledger    *Ledger
	event     Event
	attempted bool
}

// Begin reserves the largest permitted completion before returning to the effect.
func (l *Ledger) Begin(event Event) (*Operation, error) {
	event.Schema = Schema
	if event.OperationID == "" {
		event.OperationID = newID()
	}
	event.Phase, event.Authorization, event.Execution, event.Delivery, event.Outcome, event.Receipt = "admitted", "allow", "unknown", "unknown", "admitted", ""
	if err := l.Append(event); err != nil {
		return nil, err
	}
	return &Operation{ledger: l, event: event}, nil
}

// BeginCleanup spends the durable lifecycle reservation for this exact resource.
// It does not infer the outcome of an interrupted opening operation.
func (op *Operation) BeginCleanup() (*Operation, error) {
	if op == nil || op.event.CleanupAction == "" {
		return nil, fmt.Errorf("%w: missing lifecycle admission", ErrInvalid)
	}
	event := op.event
	event.EventID, event.At, event.OperationID = newID(), time.Now().UTC(), newID()
	event.Action, event.CleanupAction, event.CleanupOf = op.event.CleanupAction, "", op.event.OperationID
	return op.ledger.Begin(event)
}

func (op *Operation) Complete(execution, delivery, outcome, receipt string) error {
	if op == nil {
		return fmt.Errorf("%w: missing admission", ErrUncertain)
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.attempted {
		return fmt.Errorf("%w: completion already attempted", ErrUncertain)
	}
	op.attempted = true
	event := op.event
	event.EventID = newID()
	event.At = time.Now().UTC()
	event.Phase = "completed"
	event.Execution, event.Delivery, event.Outcome, event.Receipt = execution, delivery, outcome, receipt
	if err := op.ledger.Append(event); err != nil {
		return fmt.Errorf("%w: durable completion failed", ErrUncertain)
	}
	return nil
}

func (stats *ledgerStats) trackOperation(event Event) error {
	if event.Phase == "" || event.Phase == "decision" {
		return nil
	}
	if stats.operations == nil {
		stats.operations = map[string]Event{}
	}
	prior, exists := stats.operations[event.OperationID]
	if event.Phase == "admitted" {
		if exists {
			return fmt.Errorf("%w: operation already recorded", ErrUncertain)
		}

		if event.CleanupOf != "" {
			parent, known := stats.operations[event.CleanupOf]
			if !known || !activeLifecycle(parent) || event.Action != parent.CleanupAction || !sameCleanupScope(parent, event) {
				return fmt.Errorf("%w: cleanup parent or scope", ErrInvalid)
			}
			if stats.cleanups[event.CleanupOf] != "" {
				return fmt.Errorf("%w: cleanup already admitted", ErrUncertain)
			}
			if stats.cleanups == nil {
				stats.cleanups = map[string]string{}
			}
			stats.cleanups[event.CleanupOf] = event.OperationID
		}
		if event.CorrelationID != "" {
			for _, pending := range stats.operations {
				if unsettled(pending) && pending.OperationID != event.CleanupOf && !stats.closedLifecycle(pending.OperationID) && pending.CorrelationID == event.CorrelationID && pending.PrincipalID == event.PrincipalID && pending.ContextID == event.ContextID && pending.Environment == event.Environment && pending.Name == event.Name {
					return fmt.Errorf("%w: unsettled target", ErrUncertain)
				}
			}
		}
	} else if !exists || !unsettled(prior) || !sameOperation(prior, event) {
		return fmt.Errorf("%w: completion identity or state", ErrInvalid)
	}
	stats.operations[event.OperationID] = event
	return nil
}

func (l *Ledger) completionReserve(stats ledgerStats, bodyBytes int64, segment int, segmentBytes int64) error {
	var reserved int64
	var records uint64
	for _, event := range stats.operations {
		if unsettled(event) {
			reserved += MaxRecordBytes
			records++
		}
		if activeLifecycle(event) && stats.cleanups[event.OperationID] == "" {
			reserved += 2 * MaxRecordBytes
			records += 2
		}
	}
	if stats.bytes+bodyBytes+reserved > l.bounds.total || stats.records+1+records > l.bounds.records {
		return ErrFull
	}
	if reserved == 0 {
		return nil
	}
	available := max(int64(0), l.bounds.segment-segmentBytes)
	// Each future segment may waste up to one maximum record at its boundary.
	for i := segment + 1; i < l.bounds.segments; i++ {
		available += max(int64(0), l.bounds.segment-MaxRecordBytes)
	}
	if reserved > available {
		return ErrFull
	}
	return nil
}

// Resolve settles an unsettled operation on operator evidence. The outcome is
// declared explicitly — not_started, succeeded or failed — with a bounded
// receipt reference the operator actually verified. Without evidence the
// record stays unsettled: no TTL and no inferred outcome.
func (l *Ledger) Resolve(scope Scope, operationID, execution, evidence string) error {
	if l == nil || !operationCode(operationID) || scope.PrincipalID == "" {
		return fmt.Errorf("%w: resolution scope", ErrInvalid)
	}
	switch execution {
	case "not_started", "succeeded", "failed":
	default:
		return fmt.Errorf("%w: resolution outcome", ErrInvalid)
	}
	if evidence == "" || !ValidReceipt(evidence) {
		return fmt.Errorf("%w: resolution evidence", ErrInvalid)
	}
	var admitted Event
	err := l.locked(func(root *os.Root) error {
		stats, err := l.scan(root, nil)
		if err != nil {
			return err
		}
		pending, ok := stats.operations[operationID]
		if !ok || !unsettled(pending) || pending.PrincipalID != scope.PrincipalID ||
			(scope.ContextID != "" && pending.ContextID != scope.ContextID) ||
			(scope.Environment != "" && pending.Environment != scope.Environment) {
			return fmt.Errorf("%w: operation not unsettled", ErrUncertain)
		}
		admitted = pending
		return nil
	})
	if err != nil {
		return err
	}
	op := &Operation{ledger: l, event: admitted}
	return op.Complete(execution, "not_applicable", "operator_resolved", evidence)
}

// Pending reports unresolved durable intents without guessing whether a live
// process is still executing them. Retention never removes their evidence.
func (l *Ledger) Pending(scope Scope, limit int) ([]Event, error) {
	if l == nil || scope.PrincipalID == "" || limit < 1 || limit > MaxPageRecords {
		return nil, fmt.Errorf("%w: pending scope", ErrInvalid)
	}
	var events []Event
	err := l.locked(func(root *os.Root) error {
		stats, err := l.scan(root, nil)
		if err != nil {
			return err
		}
		for _, event := range stats.operations {
			if !unsettled(event) || event.PrincipalID != scope.PrincipalID || scope.ContextID != "" && event.ContextID != scope.ContextID || scope.Environment != "" && event.Environment != scope.Environment {
				continue
			}
			if len(events) == limit {
				return ErrPage
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].OperationID < events[j].OperationID
		}
		return events[i].At.Before(events[j].At)
	})
	return events, nil
}

func unsettled(event Event) bool { return event.Phase == "admitted" || event.Phase == "uncertain" }

func activeLifecycle(event Event) bool {
	return event.CleanupAction != "" && (unsettled(event) || event.Phase == "completed" && event.Execution == "succeeded")
}

func sameCleanupScope(parent, cleanup Event) bool {
	parent.OperationID, parent.Action = cleanup.OperationID, cleanup.Action
	parent.CleanupAction, parent.CleanupOf = cleanup.CleanupAction, cleanup.CleanupOf
	return sameOperation(parent, cleanup)
}

func (stats *ledgerStats) closedLifecycle(operationID string) bool {
	cleanup := stats.operations[stats.cleanups[operationID]]
	return cleanup.Phase == "completed" && cleanup.Execution == "succeeded"
}
