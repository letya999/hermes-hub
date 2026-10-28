package routines

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	if !commodel.IDPattern.MatchString(in.AssignmentID) || strings.TrimSpace(in.Intent) == "" || len(in.Intent) > 4096 || in.Envelope.PrincipalID != caller.PrincipalID || in.Envelope.ContextID != caller.ContextID {
		return commodel.Assignment{}, false, errors.New("invalid assignment owner or intent")
	}
	if in.Plan != nil {
		if in.Plan.Revision == 0 {
			in.Plan.Revision = 1
		}
		if err := validateAssignmentSources(in.Plan, in.Grants); err != nil {
			return commodel.Assignment{}, false, err
		}
	}
	if effectiveWakes(in.Grants) > commodel.AssignmentDefaultWakesPerDay || effectiveSources(in.Grants) > commodel.AssignmentMaxSources {
		return commodel.Assignment{}, false, errors.New("grant widening requires owner chat authorization")
	}
	path := e.AssignmentPath(in.AssignmentID)
	if b, err := os.ReadFile(path); err == nil {
		var existing commodel.Assignment
		if json.Unmarshal(b, &existing) == nil {
			if existing.Envelope.PrincipalID != caller.PrincipalID || existing.Envelope.ContextID != caller.ContextID || existing.Intent != in.Intent || existing.Title != in.Title || !sameAssignmentPlan(existing.Plan, in.Plan) || existing.Grants != in.Grants {
				return commodel.Assignment{}, false, ErrRoutineRevision
			}
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

func sameAssignmentPlan(a, b *commodel.AssignmentPlan) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
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
func (e *Engine) ApplyAssignment(id string, caller identity.Envelope, expected ...uint64) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if err := checkAssignmentRevision(item, expected); err != nil {
		return commodel.Assignment{}, err
	}
	if item.Plan == nil {
		return commodel.Assignment{}, errors.New("assignment plan required")
	}
	if len(item.Plan.Sources) == 0 {
		return commodel.Assignment{}, errors.New("assignment plan needs a source")
	}
	if err := validateAssignmentSources(item.Plan, item.Grants); err != nil {
		return commodel.Assignment{}, err
	}
	now := time.Now().UTC()
	if item.Applied == nil {
		item.Applied = map[string]commodel.AssignmentReceipt{}
	}
	wanted := make(map[string]bool, len(item.Plan.Sources))
	for _, src := range item.Plan.Sources {
		wanted[src.Name] = true
		draft := src.Schedule.Schedule()
		baseID := commodel.AssignmentScheduleID(item.AssignmentID, src.Name)
		draft.ScheduleID = baseID
		draft.AssignmentID = item.AssignmentID
		draft.AssignmentPlanRevision = item.Plan.Revision
		draft.Envelope = item.Envelope
		draft.UserID = item.Envelope.PrincipalID
		// Occurrence ownership (ValidateOccurrence) demands the actor/scope
		// pair of an owner routine — without them every tick skips silently.
		draft.ActorID = item.Envelope.PrincipalID
		draft.ScopeID = "user:" + item.Envelope.PrincipalID
		draft.OrganizationID = item.OrganizationID
		if item.Plan.Specialist != nil {
			draft.WakePrompt = item.Plan.Specialist.Instructions
		}
		rc := item.Applied[src.Name]
		if rc.ScheduleID != "" && rc.Action != "removed" {
			draft.ScheduleID = rc.ScheduleID
		}
		existing, lerr := e.LoadScheduleLocked(draft.ScheduleID)
		if lerr != nil && !os.IsNotExist(lerr) {
			return commodel.Assignment{}, lerr
		}
		if lerr == nil {
			if existing.AssignmentID != item.AssignmentID {
				return commodel.Assignment{}, ErrScheduleConflict
			}
			if existing.Creation != nil && existing.Creation.IntentSHA256 == ScheduleIntent(draft) {
				rc.ScheduleID, rc.IntentSHA, rc.Action = existing.ScheduleID, ScheduleIntent(draft), "kept"
				item.Applied[src.Name] = rc
				continue
			}
			if err := e.deleteScheduleLocked(existing.ScheduleID, item.Envelope); err != nil {
				return commodel.Assignment{}, err
			}
			rc.Action = "removed"
			item.Applied[src.Name] = rc
			if err := e.atomicJSON(e.AssignmentPath(id), item); err != nil {
				return commodel.Assignment{}, err
			}
		}
		var free bool
		for generation := 1; generation <= 1000; generation++ {
			candidate := baseID
			if generation > 1 {
				candidate = fmt.Sprintf("%s-r%d", baseID, generation)
			}
			if _, err := os.Stat(e.SchedulePath(candidate)); err != nil && !os.IsNotExist(err) {
				return commodel.Assignment{}, err
			} else if err == nil {
				continue
			}
			if _, err := os.Stat(e.CancelPath(candidate)); err != nil && !os.IsNotExist(err) {
				return commodel.Assignment{}, err
			} else if err == nil {
				continue
			}
			draft.ScheduleID, free = candidate, true
			break
		}
		if !free {
			return commodel.Assignment{}, errors.New("assignment source generations exhausted")
		}
		created, cerr := e.createScheduleAtLocked(draft, caller, "", nil, now)
		if cerr != nil {
			// Persist the partial reconciliation before reporting the error:
			// artifacts already created must stay reachable through receipts
			// or pause/cancel can never fence them (SPEC-0080 R1.8).
			item.State = "waiting"
			item.UpdatedAt = now
			_ = e.atomicJSON(e.AssignmentPath(id), item)
			return commodel.Assignment{}, cerr
		}
		item.Applied[src.Name] = commodel.AssignmentReceipt{ScheduleID: created.ScheduleID, IntentSHA: ScheduleIntent(draft), Action: "created"}
		if err := e.atomicJSON(e.AssignmentPath(id), item); err != nil {
			return commodel.Assignment{}, err
		}
	}
	for name, rc := range item.Applied {
		if wanted[name] || rc.Action == "removed" {
			continue
		}
		if err := e.deleteScheduleLocked(rc.ScheduleID, item.Envelope); err != nil && !os.IsNotExist(err) {
			return commodel.Assignment{}, err
		}
		rc.Action = "removed"
		item.Applied[name] = rc
		if err := e.atomicJSON(e.AssignmentPath(id), item); err != nil {
			return commodel.Assignment{}, err
		}
	}
	item.State = "active"
	item.AppliedRev = item.Plan.Revision
	item.Revision++
	item.UpdatedAt = now
	return item, e.atomicJSON(e.AssignmentPath(id), item)
}

// UpdateAssignmentPlan stores a new authorized plan generation; grants may
// only narrow unless authorized at the plan's own revision (SPEC-0080 R2.4).
func (e *Engine) UpdateAssignmentPlan(id string, plan commodel.AssignmentPlan, grants commodel.AssignmentGrants, caller identity.Envelope, expected ...uint64) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if err := checkAssignmentRevision(item, expected); err != nil {
		return commodel.Assignment{}, err
	}
	if item.Plan != nil && plan.Revision <= item.Plan.Revision {
		plan.Revision = item.Plan.Revision + 1
	}
	if err := validateAssignmentSources(&plan, grants); err != nil {
		return commodel.Assignment{}, err
	}
	if (effectiveWakes(grants) > effectiveWakes(item.Grants) || effectiveSources(grants) > effectiveSources(item.Grants)) && (plan.Revision == 0 || item.GrantsAuthorizedRev != plan.Revision) {
		return commodel.Assignment{}, errors.New("grant widening requires owner authorization at the plan revision")
	}
	item.Plan = &plan
	item.Grants = grants
	item.State = "waiting"
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return item, e.atomicJSON(e.AssignmentPath(id), item)
}

func checkAssignmentRevision(item commodel.Assignment, expected []uint64) error {
	if item.State == "cancelled" || item.State == "completed" {
		return errors.New("assignment is terminal")
	}
	if len(expected) > 0 && expected[0] != item.Revision {
		return ErrRoutineRevision
	}
	return nil
}

func effectiveWakes(grants commodel.AssignmentGrants) int {
	if grants.MaxWakesPerDay == 0 {
		return commodel.AssignmentDefaultWakesPerDay
	}
	return grants.MaxWakesPerDay
}

func effectiveSources(grants commodel.AssignmentGrants) int {
	if grants.MaxSources == 0 {
		return commodel.AssignmentMaxSources
	}
	return grants.MaxSources
}

func validateAssignmentSources(plan *commodel.AssignmentPlan, grants commodel.AssignmentGrants) error {
	if plan == nil || plan.Revision == 0 || grants.MaxWakesPerDay < 0 || grants.MaxSources < 0 || len(plan.Sources) > effectiveSources(grants) {
		return errors.New("invalid assignment plan or grants")
	}
	for i := range plan.Sources {
		probe := plan.Sources[i].Schedule.Probe
		if probe == nil || !strings.Contains(strings.ReplaceAll(probe.Tool, "_", "-"), "watch-chat-messages") {
			continue
		}
		normalize := func(args map[string]any) error {
			raw, ok := args["chat_id"].(string)
			if !ok {
				return nil
			}
			chat, err := telegramChatReference(raw)
			if err != nil {
				return err
			}
			args["chat_id"] = chat
			return nil
		}
		if err := normalize(probe.Args); err != nil {
			return err
		}
		for _, source := range probe.Sources {
			if err := normalize(source.Args); err != nil {
				return err
			}
		}
	}
	if len(plan.Unavailable) > 32 {
		return errors.New("too many unavailable sources")
	}
	for _, source := range plan.Unavailable {
		if strings.TrimSpace(source.Reference) == "" || strings.TrimSpace(source.Reason) == "" || len(source.Reference) > 256 || len(source.Reason) > 256 || strings.ContainsAny(source.Reference+source.Reason, "\n\r") {
			return errors.New("invalid unavailable source entry")
		}
	}
	seen := make(map[string]bool, len(plan.Sources))
	for _, src := range plan.Sources {
		if !commodel.IDPattern.MatchString(src.Name) || seen[src.Name] {
			return errors.New("invalid or duplicate assignment source name")
		}
		if src.Schedule.JobKind == "agent" {
			return errors.New("standing assignment sources require a deterministic probe, not an agent timer")
		}
		if src.Schedule.JobKind == "event" && src.Schedule.Sink == "agent" && (src.Schedule.Probe == nil || strings.TrimSpace(src.Schedule.Probe.Filter) == "") {
			return errors.New("assignment event sources that wake the agent require a deterministic filter")
		}
		seen[src.Name] = true
	}
	return nil
}

func ValidateAssignmentPlan(plan *commodel.AssignmentPlan, grants commodel.AssignmentGrants) error {
	return validateAssignmentSources(plan, grants)
}

// RecordAssignmentProgress is one specialist turn's durable result. A stale
// plan or assignment revision cannot overwrite newer work, and a repeated
// tool call from the same run is idempotent.
func (e *Engine) RecordAssignmentProgress(id string, caller identity.Envelope, expected uint64, jobID, summary, finding, nextStep string, complete bool) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if item.Progress.LastJobID == jobID && jobID != "" {
		return item, nil
	}
	if err := checkAssignmentRevision(item, []uint64{expected}); err != nil {
		return commodel.Assignment{}, err
	}
	if item.State != "active" || item.Plan == nil || item.AppliedRev != item.Plan.Revision || jobID == "" || len(summary) > 2048 || len(finding) > 512 || len(nextStep) > 1024 || (strings.TrimSpace(summary) == "" && strings.TrimSpace(finding) == "" && !complete) {
		return commodel.Assignment{}, errors.New("invalid assignment progress")
	}
	now := time.Now().UTC()
	item.Progress.Summary = summary
	item.Progress.NextStep = nextStep
	if finding != "" {
		item.Progress.Findings = append(item.Progress.Findings, finding)
		if len(item.Progress.Findings) > 20 {
			item.Progress.Findings = item.Progress.Findings[len(item.Progress.Findings)-20:]
		}
	}
	item.Progress.LastJobID = jobID
	item.Progress.UpdatedAt = now
	if complete {
		for _, rc := range item.Applied {
			if rc.Action == "removed" {
				continue
			}
			if err := e.deleteScheduleLocked(rc.ScheduleID, item.Envelope); err != nil && !os.IsNotExist(err) {
				return commodel.Assignment{}, err
			}
		}
		item.State = "completed"
	}
	item.Revision++
	item.UpdatedAt = now
	return item, e.atomicJSON(e.AssignmentPath(id), item)
}

// AssignmentForWakeLocked returns the active, applied generation while the
// shared spool/engine mutex is held.
func (e *Engine) AssignmentForWakeLocked(id string, planRev uint64) (commodel.Assignment, bool) {
	item, err := e.loadAssignmentLocked(id)
	return item, err == nil && item.State == "active" && item.AppliedRev == planRev && item.Plan != nil && item.Plan.Revision == planRev
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
	if item.Plan == nil || planRev != item.Plan.Revision+1 || grants.MaxWakesPerDay < 1 || grants.MaxSources < 1 {
		return errors.New("invalid owner grant authorization")
	}
	item.GrantsAuthorizedRev = planRev
	item.Grants = grants
	item.Revision++
	item.UpdatedAt = time.Now().UTC()
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// admitAssignmentWakeLocked is the claim-before-wake fence for
// assignment-owned schedules (SPEC-0080 R2.2). e.mu must be held.
func (e *Engine) admitAssignmentWakeLocked(assignmentID string, now time.Time, planRev ...uint64) bool {
	item, err := e.loadAssignmentLocked(assignmentID)
	if err != nil {
		return false
	}
	if item.State != "active" {
		// Paused, completed, cancelled or failed assignments never reach
		// the model (SPEC-0080 R2.2).
		return false
	}
	if len(planRev) > 0 && planRev[0] != 0 && (item.Plan == nil || item.Plan.Revision != planRev[0] || item.AppliedRev != planRev[0]) {
		return false
	}
	limit := item.Grants.MaxWakesPerDay
	if limit == 0 {
		limit = commodel.AssignmentDefaultWakesPerDay
	}
	// The counter is per owner-day: a stale WakeDay rolls over before the
	// limit check, otherwise the budget stays spent forever.
	day := now.UTC().Format("2006-01-02")
	if item.Checkpoint.WakeDay != day {
		item.Checkpoint.WakeDay, item.Checkpoint.WakesToday = day, 0
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

// AdmitAssignmentWakeLocked exposes the wake fence to the durable spool's
// event fan-out; the engine and the spool share one lock (e.mu == s.mu).
func (e *Engine) AdmitAssignmentWakeLocked(assignmentID string, now time.Time, planRev ...uint64) bool {
	return e.admitAssignmentWakeLocked(assignmentID, now, planRev...)
}

// ResumeAssignment re-arms a paused assignment's artifacts (SPEC-0080 R1.5).
func (e *Engine) ResumeAssignment(id string, caller identity.Envelope, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	if err := checkAssignmentRevision(item, expected); err != nil {
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
func (e *Engine) CancelAssignment(id string, caller identity.Envelope, expected ...uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return err
	}
	if err := checkAssignmentRevision(item, expected); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, rc := range item.Applied {
		if rc.Action == "removed" {
			continue
		}
		if err := e.deleteScheduleLocked(rc.ScheduleID, item.Envelope); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	item.State = "cancelled"
	item.CancelledAt = &now
	item.Revision++
	item.UpdatedAt = now
	return e.atomicJSON(e.AssignmentPath(id), item)
}

// CompleteAssignment retires sources like cancel but lands in the terminal
// "completed" state (SPEC-0080 R1.5) — the owner marks the watch finished.
func (e *Engine) CompleteAssignment(id string, caller identity.Envelope, expected ...uint64) (commodel.Assignment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, err := e.assignmentForCallerLocked(id, caller)
	if err != nil {
		return commodel.Assignment{}, err
	}
	if err := checkAssignmentRevision(item, expected); err != nil {
		return commodel.Assignment{}, err
	}
	now := time.Now().UTC()
	for _, rc := range item.Applied {
		if rc.Action == "removed" {
			continue
		}
		if err := e.deleteScheduleLocked(rc.ScheduleID, item.Envelope); err != nil && !os.IsNotExist(err) {
			return commodel.Assignment{}, err
		}
	}
	item.State = "completed"
	item.Revision++
	item.UpdatedAt = now
	return item, e.atomicJSON(e.AssignmentPath(id), item)
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
	if item.State == "cancelled" || item.State == "completed" {
		return errors.New("assignment is terminal")
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

var telegramUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)

// telegramChatReference accepts public chat usernames and stable numeric IDs.
// Invite and discussion links need separate access checks and cannot become watches.
func telegramChatReference(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "@")
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "t.me" && u.Hostname() != "telegram.me") {
			return "", errors.New("invalid telegram public chat reference")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "s" {
			parts = parts[1:]
		}
		if len(parts) < 1 || len(parts) > 2 || (len(parts) == 2 && !decimalMessageID(parts[1])) {
			return "", errors.New("telegram invite or discussion link requires separate access check")
		}
		raw = parts[0]
	}
	if strings.HasPrefix(raw, "-") {
		if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return raw, nil
		}
	}
	if !telegramUsername.MatchString(raw) {
		return "", errors.New("telegram invite or non-public chat cannot be applied")
	}
	return raw, nil
}

func decimalMessageID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
