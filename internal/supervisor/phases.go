package supervisor

import (
	"github.com/letya999/hermes-hub/internal/diagnostics"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// SPEC-0053 R3.1: every job lives on an explicit phase machine. Uncertainty is
// a flag on the record (Unconfirmed/UnconfirmedSince), never a phase — a job
// keeps its real position while its outcome is unconfirmed.
const (
	phaseAccepted        = "accepted"
	phaseWaking          = "waking"
	phaseAdmitted        = "admitted"
	phaseRunning         = "running"
	phaseWaitingApproval = "waiting_approval"
	phaseCompleted       = "completed"
	phaseFailed          = "failed"
	phaseCancelled       = "cancelled"
	phaseInterrupted     = "interrupted"
)

type phaseEvent struct{ To string }

var phaseTransitions = map[string]map[string]bool{
	phaseAccepted:        {phaseWaking: true, phaseAdmitted: true, phaseCancelled: true, phaseInterrupted: true, phaseFailed: true},
	phaseWaking:          {phaseAdmitted: true, phaseRunning: true, phaseCancelled: true, phaseInterrupted: true, phaseFailed: true},
	phaseAdmitted:        {phaseRunning: true, phaseCancelled: true, phaseInterrupted: true, phaseFailed: true},
	phaseRunning:         {phaseWaitingApproval: true, phaseCompleted: true, phaseFailed: true, phaseCancelled: true, phaseInterrupted: true},
	phaseWaitingApproval: {phaseRunning: true, phaseCompleted: true, phaseFailed: true, phaseCancelled: true, phaseInterrupted: true},
}

var terminalPhases = map[string]bool{
	phaseCompleted: true, phaseFailed: true, phaseCancelled: true, phaseInterrupted: true,
}

// setJobPhase moves a registered job through the transition table. Phase is
// derived state — a rejected move only logs, it never rewrites the record.
func (m *Manager) setJobPhase(request hubruntime.ExecuteRequest, phase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := executeJobKey(request)
	if record, ok := m.jobs[key]; ok {
		transitionJobPhase(key, &record, phase)
		m.jobs[key] = record
	}
}

// transitionJobPhase applies a phase move through the table; an illegal move
// keeps the current phase and is logged — a stale producer must not silently
// reposition a job.
func transitionJobPhase(key string, record *jobRecord, target string) {
	mapLegacyPhase(record)
	to, ok := nextPhase(record.Phase, phaseEvent{To: target})
	if !ok {
		diagnostics.Logger("supervisor").Warn("rejected phase transition", "job_id", key, "from", record.Phase, "to", target)
		return
	}
	record.Phase = to
}

func nextPhase(from string, event phaseEvent) (string, bool) {
	if event.To == from {
		_, valid := phaseTransitions[from]
		return from, valid || terminalPhases[from]
	}
	return event.To, phaseTransitions[from][event.To]
}

// mapLegacyPhase fills phase/unconfirmed fields for records persisted before
// the phase machine existed. Position is derived from binding state: run_id
// means the runtime admitted work, a bare generation means bound but not yet
// observed, neither means still at acceptance.
func mapLegacyPhase(record *jobRecord) {
	if record.Phase == "" {
		switch {
		case terminalPhases[statusPhase(record.Status)]:
			record.Phase = statusPhase(record.Status)
		case record.Status == "waiting_for_approval":
			record.Phase = phaseWaitingApproval
		case record.Response.RunID != "":
			record.Phase = phaseRunning
		case record.Generation != "":
			record.Phase = phaseAdmitted
		default:
			record.Phase = phaseAccepted
		}
	}
	if record.Status == "uncertain" && !record.Unconfirmed {
		record.Unconfirmed = true
		record.UnconfirmedSince = record.UpdatedAt
	}
	// Records persisted before the status vocabulary stayed at Status "":
	// Dispatching marks work already sent for execution, so a dispatched record
	// is unconfirmed residue owned by the reconciler. A non-dispatching one was
	// never sent and remains a normal queued job.
	if record.Status == "" && record.Dispatching && !record.Unconfirmed {
		record.Status = "uncertain"
		record.Unconfirmed = true
		record.UnconfirmedSince = record.UpdatedAt
	}
}

// statusPhase maps the wire status vocabulary onto phases. Uncertain maps to
// running — the job kept its phase while confirmation was lost.
func statusPhase(status string) string {
	switch status {
	case "completed":
		return phaseCompleted
	case "failed":
		return phaseFailed
	case "cancelled":
		return phaseCancelled
	case "interrupted":
		return phaseInterrupted
	case "waiting_for_approval":
		return phaseWaitingApproval
	case "uncertain", "running":
		return phaseRunning
	default:
		return phaseAccepted
	}
}
