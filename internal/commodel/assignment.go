package commodel

import (
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Assignment is the durable owner instruction behind a set of routine
// artifacts (SPEC-0080): it binds the verified owner envelope — never a
// Hermes session — and carries the intent text verbatim, the validated
// plan, narrowed grants and durable counters. It is state, not a process.
type Assignment struct {
	AssignmentID string            `json:"assignment_id"`
	Revision     uint64            `json:"revision"`
	Envelope     identity.Envelope `json:"envelope"`
	// Intent is the owner's verbatim instruction; append-only per record —
	// a changed intent is a new assignment.
	Intent     string `json:"intent"`
	IntentHash string `json:"intent_hash"`
	Title      string `json:"title"`
	// State: draft|active|waiting|paused|completed|cancelled|failed
	// (SPEC-0080 R1.2). waiting = applied but a link is blocked or missing.
	State string `json:"state"`
	// Plan is the latest owner-authorized revision; AppliedRev is the plan
	// revision currently provisioned into routine artifacts.
	Plan       *AssignmentPlan              `json:"plan,omitempty"`
	AppliedRev uint64                       `json:"applied_rev,omitempty"`
	Applied    map[string]AssignmentReceipt `json:"applied,omitempty"`
	Grants     AssignmentGrants             `json:"grants"`
	// GrantsAuthorizedRev records the plan revision the verified owner
	// authorized through the chat-command path; a plan update may widen
	// grants only while authorized at its own revision (SPEC-0080 R2.4).
	GrantsAuthorizedRev uint64               `json:"grants_authorized_rev,omitempty"`
	Checkpoint          AssignmentCheckpoint `json:"checkpoint"`
	Blocked             string               `json:"blocked,omitempty"`
	CancelledAt         *time.Time           `json:"cancelled_at,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
}

// AssignmentReceipt is the durable per-source provisioning record left by
// apply: name → artifact id + the intent the artifact was built from, so a
// replayed apply recognizes its own work and never duplicates it.
type AssignmentReceipt struct {
	ScheduleID string `json:"schedule_id"`
	IntentSHA  string `json:"intent_sha"`
	Action     string `json:"action"` // created|kept|replaced|removed
}

// AssignmentPlan is one authorized generation of the assignment's desired
// artifacts and its wake specialist (SPEC-0080 R1.3).
type AssignmentPlan struct {
	Revision   uint64             `json:"revision"`
	Sources    []AssignmentSource `json:"sources"`
	Specialist *SpecialistSpec    `json:"specialist,omitempty"`
}

// AssignmentSource names one owned routine artifact. The embedded draft is
// validated by the existing schedule draft validator; the engine stamps
// identity (schedule_id, envelope, assignment fields) — drafts never set it.
type AssignmentSource struct {
	Name     string       `json:"name"`
	Schedule RoutineDraft `json:"schedule"`
}

// SpecialistSpec describes the bounded wake specialist: instructions that
// prefix every evidence job plus the reply policy. It is a descriptor, not
// a process — one spec serves every source and any number of wakes.
type SpecialistSpec struct {
	Instructions string `json:"instructions"`
	// Reply: "auto" (default, the specialist may reply within grant bounds)
	// or "approval" (replies must pass the existing approval path).
	Reply string `json:"reply,omitempty"`
}

// AssignmentGrants bound what the assignment may spend; the agent may only
// ever narrow them (SPEC-0080 R2.4).
type AssignmentGrants struct {
	// MaxWakesPerDay bounds agent-sink admissions per owner-day;
	// 0 selects the default (AssignmentDefaultWakesPerDay).
	MaxWakesPerDay int `json:"max_wakes_per_day,omitempty"`
	// MaxSources bounds plan sources; 0 selects AssignmentMaxSources.
	MaxSources int `json:"max_sources,omitempty"`
}

// AssignmentCheckpoint is the durable measurement card: wakes, model calls
// (a wake IS a model call), notices and the last observed wake latency.
type AssignmentCheckpoint struct {
	LastWakeAt        time.Time `json:"last_wake_at,omitempty"`
	LastWakeLatencyMs int64     `json:"last_wake_latency_ms,omitempty"`
	WakeDay           string    `json:"wake_day,omitempty"`
	WakesToday        int       `json:"wakes_today,omitempty"`
	LLMCalls          int       `json:"llm_calls,omitempty"`
	NotifyDay         string    `json:"notify_day,omitempty"`
	NoticesToday      int       `json:"notices_today,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
}

// AssignmentDefaultWakesPerDay bounds specialist wakes when the owner did
// not set MaxWakesPerDay (SPEC-0080 R2.3).
const AssignmentDefaultWakesPerDay = 20

// AssignmentScheduleID derives the deterministic artifact id a source name
// provisions to (SPEC-0080 R1.4).
func AssignmentScheduleID(assignmentID, name string) string {
	return "asg-" + assignmentID + "-" + name
}
