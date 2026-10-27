package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ErrAssignmentNotFound is the uniform refusal for missing and foreign-owned
// assignment records — ownership probing must not leak existence.
var ErrAssignmentNotFound = errors.New("assignment not found")

// AssignmentPath resolves the durable record path; the id is filename-safe
// by validation, anything else maps through the keyed FileID hash.
func (e *Engine) AssignmentPath(id string) string {
	return filepath.Join(e.root, "assignments", commodel.FileID(id)+".json")
}

// CreateAssignment durably registers one owner instruction; a replay with
// the same id returns the stored record (idempotent create, SPEC-0080 R1.1).
func (e *Engine) CreateAssignment(in commodel.Assignment, caller identity.Envelope) (commodel.Assignment, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := e.AssignmentPath(in.AssignmentID)
	if b, err := os.ReadFile(path); err == nil {
		var existing commodel.Assignment
		if json.Unmarshal(b, &existing) == nil {
			return existing, true, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return commodel.Assignment{}, false, err
	}
	if err := e.atomicJSON(path, in); err != nil {
		return commodel.Assignment{}, false, err
	}
	return in, false, nil
}

// loadAssignmentLocked reads the durable record; a corrupt or id-mismatched
// file is invalid state, never silently repaired.
func (e *Engine) loadAssignmentLocked(id string) (commodel.Assignment, error) {
	b, err := os.ReadFile(e.AssignmentPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return commodel.Assignment{}, ErrAssignmentNotFound
		}
		return commodel.Assignment{}, err
	}
	var item commodel.Assignment
	if json.Unmarshal(b, &item) != nil || item.AssignmentID != id {
		return commodel.Assignment{}, errors.New("invalid assignment record")
	}
	return item, nil
}

// assignmentForCallerLocked loads and verifies full envelope ownership:
// principal+context scoping as in schedules, with runtime/policy pinned so a
// stale generation of the same principal cannot act (ADR-0082 D1).
func (e *Engine) assignmentForCallerLocked(id string, caller identity.Envelope) (commodel.Assignment, error) {
	item, err := e.loadAssignmentLocked(id)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
		return commodel.Assignment{}, ErrAssignmentNotFound
	}
	return item, nil
}

func (e *Engine) LoadAssignment(id string, caller identity.Envelope) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.assignmentForCallerLocked(id, caller)
}

// ListAssignments returns the caller's records only (SPEC-0080 R3.1):
// foreign records are filtered out, never surfaced as errors.
func (e *Engine) ListAssignments(caller identity.Envelope) ([]commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "assignments"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []commodel.Assignment
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var item commodel.Assignment
		b, err := os.ReadFile(filepath.Join(e.root, "assignments", entry.Name()))
		if err != nil || json.Unmarshal(b, &item) != nil || item.AssignmentID == "" {
			continue
		}
		if item.Envelope.PrincipalID != caller.PrincipalID || item.Envelope.ContextID != caller.ContextID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// ApplyAssignment reconciles the plan into owned routine artifacts
// (SPEC-0080 R1.4): deterministic per-name schedule ids, durable receipts,
// idempotent replay.
func (e *Engine) ApplyAssignment(id string, caller identity.Envelope) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	now := time.Now().UTC()
	if item.Applied == nil {
		item.Applied = map[string]commodel.AssignmentReceipt{}
	}
	for _, src := range item.Plan.Sources {
		draft := src.Schedule.Schedule()
		draft.ScheduleID = commodel.AssignmentScheduleID(item.AssignmentID, src.Name)
		draft.AssignmentID = item.AssignmentID
		draft.Envelope = item.Envelope
		draft.UserID = item.Envelope.PrincipalID
		if item.Plan.Specialist != nil {
			draft.WakePrompt = item.Plan.Specialist.Instructions
		}
		existing, lerr := e.LoadScheduleLocked(draft.ScheduleID)
		if lerr == nil && existing.AssignmentID == item.AssignmentID {
			rc := item.Applied[src.Name]
			rc.ScheduleID, rc.IntentSHA, rc.Action = existing.ScheduleID, ScheduleIntent(draft), "kept"
			item.Applied[src.Name] = rc
			continue
		}
		created, cerr := e.createScheduleAtLocked(draft, caller, "", nil, now)
		if errors.Is(cerr, ErrScheduleConflict) {
			// A retired artifact id stays retired; recreation takes the next
			// deterministic generation suffix (SPEC-0080 R1.8).
			draft.ScheduleID += "-r2"
			created, cerr = e.createScheduleAtLocked(draft, caller, "", nil, now)
		}
		if cerr != nil {
			return commodel.Assignment{}, cerr
		}
		item.Applied[src.Name] = commodel.AssignmentReceipt{ScheduleID: created.ScheduleID, IntentSHA: ScheduleIntent(draft), Action: "created"}
	}
	item.State = "active"
	item.AppliedRev = item.Revision
	item.Revision++
	item.UpdatedAt = now
	return item, e.atomicJSON(e.AssignmentPath(id), item)
}

// UpdateAssignmentPlan stores a new authorized plan generation; grants may
// only narrow unless authorized at the plan's own revision (SPEC-0080 R2.4).
func (e *Engine) UpdateAssignmentPlan(id string, plan commodel.AssignmentPlan, grants commodel.AssignmentGrants, caller identity.Envelope) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if grants.MaxWakesPerDay > item.Grants.MaxWakesPerDay && item.GrantsAuthorizedRev != plan.Revision {
		return commodel.Assignment{}, errors.New("grant widening requires owner authorization at the plan revision")
	}
	item.Plan = &plan
	item.Grants = grants
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return item, e.atomicJSON(e.AssignmentPath(id), item)
}

// AuthorizeAssignmentGrants is the owner chat-command path stamping the plan
// revision whose widening was explicitly confirmed (SPEC-0080 R2.4).
func (e *Engine) AuthorizeAssignmentGrants(id string, planRev uint64, grants commodel.AssignmentGrants, caller identity.Envelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	item.GrantsAuthorizedRev = planRev
	item.Grants = grants
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// admitAssignmentWakeLocked is the claim-before-wake fence for
// assignment-owned schedules (SPEC-0080 R2.2). e.mu must be held.
func (e *Engine) admitAssignmentWakeLocked(assignmentID string, now time.Time) bool {
	item, err := e.loadAssignmentLocked(assignmentID)
	if err != nil {
		return false
	}
	limit := item.Grants.MaxWakesPerDay
	if limit == 0 {
		limit = commodel.AssignmentDefaultWakesPerDay
	}
	if item.Checkpoint.WakesToday >= limit {
		return false
	}
	item.Checkpoint.WakesToday++
	item.Checkpoint.LLMCalls++
	item.Checkpoint.LastWakeAt = now.UTC()
	item.UpdatedAt = now.UTC()
	return e.atomicJSON(e.AssignmentPath(item.AssignmentID), item) == nil
}

// ResumeAssignment re-arms a paused assignment's artifacts (SPEC-0080 R1.5).
func (e *Engine) ResumeAssignment(id string, caller identity.Envelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, rc := range item.Applied {
		if rc.Action == "removed" {
			continue
		}
		sched, err := e.LoadScheduleLocked(rc.ScheduleID)
		if err != nil {
			continue
		}
		loc, lerr := time.LoadLocation(sched.Timezone)
		if lerr != nil {
			return lerr
		}
		next, nerr := NextSchedule(sched, loc, now)
		if nerr != nil {
			return nerr
		}
		sched.Paused = false
		sched.NextDue = next
		sched.Revision++
		sched.UpdatedAt = now
		if err := e.atomicJSON(e.SchedulePath(sched.ScheduleID), sched); err != nil {
			return err
		}
	}
	item.State = "active"
	item.Revision++
	item.UpdatedAt = now
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// CancelAssignment durably retires the assignment and its artifacts
// (SPEC-0080 R1.5): schedules are removed through the cancel path so their
// ids stay retired, and stale wakes are fenced by the terminal state.
func (e *Engine) CancelAssignment(id string, caller identity.Envelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, rc := range item.Applied {
		if err := e.deleteScheduleLocked(rc.ScheduleID, item.Envelope); err != nil {
			return err
		}
	}
	item.State = "cancelled"
	item.CancelledAt = &now
	item.Revision++
	item.UpdatedAt = now
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// PauseAssignment flips an applied assignment to paused (SPEC-0080 R1.5).
func (e *Engine) PauseAssignment(id string, caller identity.Envelope, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	if len(expected) > 0 && expected[0] != item.Revision {
		return ErrRoutineRevision
	}
	now := time.Now().UTC()
	for _, rc := range item.Applied {
		if rc.Action == "removed" {
			continue
		}
		sched, err := e.LoadScheduleLocked(rc.ScheduleID)
		if err != nil || sched.Paused {
			continue
		}
		sched.Paused = true
		sched.Revision++
		sched.UpdatedAt = now
		if err := e.atomicJSON(e.SchedulePath(sched.ScheduleID), sched); err != nil {
			return err
		}
	}
	item.State = "paused"
	item.Revision++
	item.UpdatedAt = now
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// ReconcileAssignments sweeps applied assignments for artifact drift
// (SPEC-0080 R1.8): a missing artifact puts the record into waiting with a
// blocked reason instead of silently recreating it.
func (e *Engine) ReconcileAssignments(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(e.root, "assignments"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var item commodel.Assignment
		b, err := os.ReadFile(filepath.Join(e.root, "assignments", entry.Name()))
		if err != nil || json.Unmarshal(b, &item) != nil || item.AssignmentID == "" {
			continue
		}
		if item.State != "active" {
			continue
		}
		var missing []string
		for name, rc := range item.Applied {
			if _, err := e.LoadScheduleLocked(rc.ScheduleID); err != nil {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			continue
		}
		item.State = "waiting"
		item.Blocked = "artifact removed out of band: " + strings.Join(missing, ",")
		item.Revision++
		item.UpdatedAt = now
		if err := e.atomicJSON(e.AssignmentPath(item.AssignmentID), item); err != nil {
			return err
		}
	}
	return nil
}
