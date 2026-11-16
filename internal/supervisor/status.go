package supervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Legible status contract (issue #209): every runtime reports a one-word
// phase plus named conditions with machine-checkable reasons, derived
// purely from the fields the supervisor already persists — status reads
// identical to what supervisor.json holds, so hubctl renders exactly what
// the control plane believes. The vocabulary is closed: missing or
// unrecognized inputs collapse to Unknown rather than a guessed string.
// Conditions carry no content, no secrets — ids, states and reasons only.

// Runtime phases.
const (
	PhasePending     = "pending"     // desired, waiting for spawn/readiness
	PhaseRunning     = "running"     // health + readiness good
	PhaseDegraded    = "degraded"    // alive but readiness/connectors degraded
	PhaseSuspended   = "suspended"   // administrative hold
	PhaseFailed      = "failed"      // dead container or exhausted crash budget
	PhaseStopped     = "stopped"     // stopped or not desired
	PhaseTerminating = "terminating" // stop in flight
	PhaseUnknown     = "unknown"     // observed state unrecognized
)

// Condition names — closed set.
const (
	CondReady       = "ready"              // hermes readiness probe
	CondOwnership   = "ownership_verified" // container belongs to this supervisor
	CondConnectors  = "connectors"         // connector health rollup
	CondCrashBudget = "crash_budget"       // restart budget not exhausted
)

// Condition reasons — closed vocabulary.
const (
	ReasonReady                = "ready"
	ReasonReadinessDegraded    = "readiness_degraded"
	ReasonReadinessUnavailable = "readiness_unavailable"
	ReasonNotChecked           = "not_checked"
	ReasonVerified             = "verified"
	ReasonUnverified           = "unverified"
	ReasonAbsent               = "absent"
	ReasonHealthy              = "healthy"
	ReasonDegraded             = "degraded"
	ReasonUnknown              = "unknown"
	ReasonNotConfigured        = "not_configured"
	ReasonWithinBudget         = "within_budget"
	ReasonBudgetExhausted      = "budget_exhausted"
	ReasonRetryDeferred        = "retry_deferred"
	ReasonAdmitted             = "admitted"
	ReasonNotAdmitted          = "not_admitted"
	ReasonConfirmed            = "confirmed"
	ReasonUnconfirmed          = "unconfirmed"
)

// Job condition names — the job phase machine itself lives in phases.go;
// conditions add what a bare phase cannot say.
const (
	CondJobAdmitted = "admitted" // bound to a runtime generation/run
	CondJobOutcome  = "outcome"  // terminal outcome observed vs unconfirmed
)

type Condition struct {
	Name   string    `json:"name"`
	OK     bool      `json:"ok"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since,omitempty"`
}

// RuntimeStatus is the #209 projection of one runtimeEntry: a one-word
// phase plus named conditions, safe to render in chat and API.
type RuntimeStatus struct {
	PrincipalID string      `json:"principal_id"`
	ContextID   string      `json:"context_id"`
	RuntimeID   string      `json:"runtime_id,omitempty"`
	Generation  string      `json:"generation"`
	RuntimeMode string      `json:"runtime_mode,omitempty"`
	Phase       string      `json:"phase"`
	Desired     bool        `json:"desired"`
	CheckedAt   time.Time   `json:"checked_at,omitempty"`
	Conditions  []Condition `json:"conditions"`
	CrashCount  int         `json:"crash_count,omitempty"`
	NextRetryAt time.Time   `json:"next_retry_at,omitempty"`
	CPUPercent  float64     `json:"cpu_percent,omitempty"`
	MemBytes    int64       `json:"mem_bytes,omitempty"`
}

// JobStatus projects a jobRecord: phase from the existing phase machine,
// plus admitted/outcome conditions a bare phase cannot express.
type JobStatus struct {
	JobID       string      `json:"job_id"`
	PrincipalID string      `json:"principal_id"`
	ContextID   string      `json:"context_id"`
	Phase       string      `json:"phase"`
	Status      string      `json:"status,omitempty"`
	Conditions  []Condition `json:"conditions,omitempty"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// StatusView is the bounded control-plane status document.
type StatusView struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Watchdog    WatchdogMetrics      `json:"watchdog"`
	Runtimes    []RuntimeStatus      `json:"runtimes"`
	Holds       []AdministrativeHold `json:"holds,omitempty"`
	Jobs        JobSummary           `json:"jobs"`
	Backups     []BackupRecord       `json:"backups,omitempty"`
}

type JobSummary struct {
	Total       int            `json:"total"`
	ByPhase     map[string]int `json:"by_phase"`
	Unconfirmed int            `json:"unconfirmed"`
	Open        []JobStatus    `json:"open,omitempty"`
}

func crashBudgetExhausted(rt *Runtime, now time.Time) bool {
	if rt.CrashWindowStart.IsZero() || now.Sub(rt.CrashWindowStart) >= 10*time.Minute {
		return false
	}
	return rt.CrashCount >= 3 || now.Before(rt.NextRetryAt)
}

// runtimeStatusLocked derives the #209 projection from persisted fields
// only. It never inspects new sources — a stale or absent probe is itself
// a condition (not_checked / absent), not a guess.
func runtimeStatusLocked(e *runtimeEntry, held bool, now time.Time) RuntimeStatus {
	rt := e.Runtime
	st := RuntimeStatus{
		PrincipalID: e.binding.PrincipalID,
		ContextID:   e.binding.ContextID,
		RuntimeID:   rt.RuntimeID,
		Generation:  e.Generation,
		RuntimeMode: rt.RuntimeMode,
		Desired:     rt.Desired,
		CheckedAt:   rt.HealthCheckedAt,
		CrashCount:  rt.CrashCount,
		NextRetryAt: rt.NextRetryAt,
		CPUPercent:  rt.CPUPercent,
		MemBytes:    rt.MemBytes,
	}
	probe := rt.HealthCheckedAt

	var ready Condition
	switch rt.HermesReadiness {
	case "ready":
		ready = Condition{Name: CondReady, OK: true, Reason: ReasonReady, Since: probe}
	case "degraded":
		ready = Condition{Name: CondReady, Reason: ReasonReadinessDegraded, Since: probe}
	case "unavailable":
		ready = Condition{Name: CondReady, Reason: ReasonReadinessUnavailable, Since: probe}
	default:
		ready = Condition{Name: CondReady, Reason: ReasonNotChecked, Since: probe}
	}
	var own Condition
	switch rt.Ownership {
	case "verified":
		own = Condition{Name: CondOwnership, OK: true, Reason: ReasonVerified, Since: probe}
	case "absent":
		own = Condition{Name: CondOwnership, Reason: ReasonAbsent, Since: probe}
	case "unverified":
		own = Condition{Name: CondOwnership, Reason: ReasonUnverified, Since: probe}
	default:
		own = Condition{Name: CondOwnership, Reason: ReasonNotChecked, Since: probe}
	}
	var conn Condition
	switch rt.ConnectorHealth {
	case "healthy":
		conn = Condition{Name: CondConnectors, OK: true, Reason: ReasonHealthy, Since: probe}
	case "degraded":
		conn = Condition{Name: CondConnectors, Reason: ReasonDegraded, Since: probe}
	case "not_configured":
		conn = Condition{Name: CondConnectors, OK: true, Reason: ReasonNotConfigured, Since: probe}
	case "unknown":
		conn = Condition{Name: CondConnectors, Reason: ReasonUnknown, Since: probe}
	default:
		conn = Condition{Name: CondConnectors, Reason: ReasonNotChecked, Since: probe}
	}
	var budget Condition
	switch {
	case crashBudgetExhausted(&rt, now):
		budget = Condition{Name: CondCrashBudget, Reason: ReasonBudgetExhausted, Since: rt.CrashWindowStart}
	case !rt.NextRetryAt.IsZero() && now.Before(rt.NextRetryAt):
		budget = Condition{Name: CondCrashBudget, Reason: ReasonRetryDeferred, Since: rt.CrashWindowStart}
	default:
		budget = Condition{Name: CondCrashBudget, OK: true, Reason: ReasonWithinBudget}
	}
	st.Conditions = []Condition{ready, own, conn, budget}

	health := rt.RuntimeHealth
	switch {
	case held:
		st.Phase = PhaseSuspended
	case e.State == Stopping:
		st.Phase = PhaseTerminating
	case !rt.Desired:
		// wind-down: a still-alive container is terminating, anything
		// already gone is stopped
		switch health {
		case "running", "created", "restarting":
			st.Phase = PhaseTerminating
		default:
			st.Phase = PhaseStopped
		}
	case crashBudgetExhausted(&rt, now):
		st.Phase = PhaseFailed
	case health == "exited" || health == "dead":
		st.Phase = PhaseFailed
	case health == "unknown":
		st.Phase = PhaseUnknown
	case health == "running":
		switch {
		case rt.HermesReadiness == "degraded" || rt.HermesReadiness == "unavailable" || rt.ConnectorHealth == "degraded":
			st.Phase = PhaseDegraded
		case rt.HermesReadiness == "ready":
			st.Phase = PhaseRunning
		default:
			st.Phase = PhasePending
		}
	case health == "missing" || health == "stopped" || health == "created" || health == "restarting" || health == "paused":
		st.Phase = PhasePending
	default:
		// never probed or unrecognized docker state — pending until the
		// next probe rather than a guessed string
		st.Phase = PhasePending
	}
	return st
}

// jobStatusLocked projects a jobRecord. Phase comes from the phase machine;
// unconfirmed stays a condition (outcome=unconfirmed), never a phase.
func jobStatusLocked(r jobRecord) JobStatus {
	st := JobStatus{
		JobID:       r.Request.JobID,
		PrincipalID: r.Request.PrincipalID,
		ContextID:   r.Request.ContextID,
		Phase:       r.Phase,
		Status:      r.Status,
		UpdatedAt:   r.UpdatedAt,
	}
	if st.Phase == "" {
		st.Phase = PhaseUnknown
	}
	admitted := r.Generation != "" || r.Response.RunID != ""
	admitReason := ReasonNotAdmitted
	if admitted {
		admitReason = ReasonAdmitted
	}
	st.Conditions = []Condition{{Name: CondJobAdmitted, OK: admitted, Reason: admitReason}}
	if r.Unconfirmed {
		st.Conditions = append(st.Conditions, Condition{Name: CondJobOutcome, Reason: ReasonUnconfirmed, Since: r.UnconfirmedSince})
	} else {
		st.Conditions = append(st.Conditions, Condition{Name: CondJobOutcome, OK: true, Reason: ReasonConfirmed})
	}
	return st
}

// StatusView returns the bounded control-plane projection — runtimes with
// phase+conditions, holds, and the job ledger reduced to per-phase counts
// plus open (non-terminal) job details.
func (m *Manager) StatusView() StatusView {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	view := StatusView{
		GeneratedAt: now.UTC(),
		Watchdog:    m.watchdog,
		Jobs:        JobSummary{ByPhase: map[string]int{}},
	}
	for _, e := range m.items {
		held := m.holds[holdKey(e.binding.PrincipalID, e.binding.ContextID)].Active
		view.Runtimes = append(view.Runtimes, runtimeStatusLocked(e, held, now))
	}
	sort.Slice(view.Runtimes, func(i, j int) bool {
		if view.Runtimes[i].PrincipalID != view.Runtimes[j].PrincipalID {
			return view.Runtimes[i].PrincipalID < view.Runtimes[j].PrincipalID
		}
		return view.Runtimes[i].ContextID < view.Runtimes[j].ContextID
	})
	for _, hold := range m.holds {
		if hold.Active {
			view.Holds = append(view.Holds, hold)
		}
	}
	for _, record := range m.jobs {
		phase := record.Phase
		if phase == "" {
			phase = PhaseUnknown
		}
		view.Jobs.ByPhase[phase]++
		view.Jobs.Total++
		if record.Unconfirmed {
			view.Jobs.Unconfirmed++
		}
		if !terminalPhases[phase] {
			view.Jobs.Open = append(view.Jobs.Open, jobStatusLocked(record))
		}
	}
	sort.Slice(view.Jobs.Open, func(i, j int) bool { return view.Jobs.Open[i].JobID < view.Jobs.Open[j].JobID })
	for _, rec := range m.backups {
		view.Backups = append(view.Backups, rec)
	}
	sort.Slice(view.Backups, func(i, j int) bool { return view.Backups[i].User < view.Backups[j].User })
	return view
}

// Metrics renders the status document as bounded prometheus text (#199) —
// loopback scraping, closed label sets, never job content or secrets.
func (v StatusView) Metrics(w io.Writer) {
	fmt.Fprintf(w, "# HELP hub_supervisor_runtimes Runtimes by legible phase.\n# TYPE hub_supervisor_runtimes gauge\n")
	byPhase := map[string]int{}
	for _, rt := range v.Runtimes {
		byPhase[rt.Phase]++
	}
	for _, phase := range []string{PhasePending, PhaseRunning, PhaseDegraded, PhaseSuspended, PhaseFailed, PhaseStopped, PhaseTerminating, PhaseUnknown} {
		fmt.Fprintf(w, "hub_supervisor_runtimes{phase=%q} %d\n", phase, byPhase[phase])
	}
	fmt.Fprintf(w, "# HELP hub_supervisor_crash_budget_exhausted Runtimes whose crash budget is consumed.\n# TYPE hub_supervisor_crash_budget_exhausted gauge\n")
	exhausted := 0
	for _, rt := range v.Runtimes {
		for _, c := range rt.Conditions {
			if c.Name == CondCrashBudget && c.Reason == ReasonBudgetExhausted {
				exhausted++
			}
		}
	}
	fmt.Fprintf(w, "hub_supervisor_crash_budget_exhausted %d\n", exhausted)
	fmt.Fprintf(w, "# HELP hub_supervisor_jobs Supervisor job ledger by phase.\n# TYPE hub_supervisor_jobs gauge\n")
	for _, phase := range []string{phaseAccepted, phaseWaking, phaseAdmitted, phaseRunning, phaseWaitingApproval, phaseCompleted, phaseFailed, phaseCancelled, phaseInterrupted, PhaseUnknown} {
		fmt.Fprintf(w, "hub_supervisor_jobs{phase=%q} %d\n", phase, v.Jobs.ByPhase[phase])
	}
	fmt.Fprintf(w, "# HELP hub_supervisor_jobs_unconfirmed Jobs whose outcome is unconfirmed.\n# TYPE hub_supervisor_jobs_unconfirmed gauge\n")
	fmt.Fprintf(w, "hub_supervisor_jobs_unconfirmed %d\n", v.Jobs.Unconfirmed)
	fmt.Fprintf(w, "# HELP hub_supervisor_holds_active Active administrative holds.\n# TYPE hub_supervisor_holds_active gauge\n")
	fmt.Fprintf(w, "hub_supervisor_holds_active %d\n", len(v.Holds))
	fmt.Fprintf(w, "# HELP hub_supervisor_watchdog_sweeps Watchdog sweeps since boot.\n# TYPE hub_supervisor_watchdog_sweeps counter\n")
	fmt.Fprintf(w, "hub_supervisor_watchdog_sweeps %d\n", v.Watchdog.Sweeps)
	fmt.Fprintf(w, "# HELP hub_supervisor_watchdog_last_sweep_ms Duration of the last watchdog sweep in milliseconds.\n# TYPE hub_supervisor_watchdog_last_sweep_ms gauge\n")
	fmt.Fprintf(w, "hub_supervisor_watchdog_last_sweep_ms %d\n", v.Watchdog.LastSweepMS)
	fmt.Fprintf(w, "# HELP hub_supervisor_backup_last_success_seconds Unix time of the space's last successful backup.\n# TYPE hub_supervisor_backup_last_success_seconds gauge\n")
	for _, rec := range v.Backups {
		var last int64
		if !rec.LastSuccessAt.IsZero() {
			last = rec.LastSuccessAt.Unix()
		}
		fmt.Fprintf(w, "hub_supervisor_backup_last_success_seconds{user=%q} %d\n", rec.User, last)
	}
	fmt.Fprintf(w, "# HELP hub_supervisor_backup_drill_ok Restore-drill outcome per space (1 = last drill passed).\n# TYPE hub_supervisor_backup_drill_ok gauge\n")
	for _, rec := range v.Backups {
		ok := 0
		if rec.DrillResult == "ok" {
			ok = 1
		}
		fmt.Fprintf(w, "hub_supervisor_backup_drill_ok{user=%q} %d\n", rec.User, ok)
	}
}

// --- transitions journal (#209): watch-able stream --------------------------
//
// One allowlisted line per observed phase/condition change — transitions are
// events, not polled diffs. Emitted from persistLocked so every durable
// mutation (reconcile, ensure, job phase, holds, unconfirmed) journals once
// and identically to the persisted record. Same 4 MiB bounded rotation as
// signals.jsonl; an in-memory signature map dedups steady state.

const transitionsFile = "transitions.jsonl"

type statusTransition struct {
	At          time.Time   `json:"at"`
	Kind        string      `json:"kind"` // runtime | job
	ID          string      `json:"id"`
	PrincipalID string      `json:"principal_id"`
	ContextID   string      `json:"context_id"`
	Generation  string      `json:"generation,omitempty"`
	Phase       string      `json:"phase"`
	Changes     []Condition `json:"changes,omitempty"`
}

func (m *Manager) transitionsPath() string {
	return filepath.Join(m.cfg.StateDir, transitionsFile)
}

func statusSignature(phase string, conditions []Condition) string {
	sig := phase
	for _, c := range conditions {
		sig += "|" + c.Name + "=" + c.Reason
	}
	return sig
}

// emitTransitionsLocked diffs the projected signature for key against the
// last-emitted one; on change it appends one bounded line naming only the
// conditions that moved since the last journal entry.
func (m *Manager) emitTransitionsLocked(kind, id, principalID, contextID, generation, phase string, conditions []Condition) {
	key := kind + ":" + id
	sig := statusSignature(phase, conditions)
	if m.statusSig == nil {
		m.statusSig = map[string]string{}
	}
	prev := m.statusSig[key]
	if prev == sig {
		return
	}
	m.statusSig[key] = sig

	changes := make([]Condition, 0, len(conditions))
	for _, c := range conditions {
		if prev != "" && strings.Contains(prev, "|"+c.Name+"="+c.Reason) {
			continue
		}
		changes = append(changes, c)
	}
	m.appendTransitionLocked(statusTransition{
		At:          m.now().UTC(),
		Kind:        kind,
		ID:          id,
		PrincipalID: principalID,
		ContextID:   contextID,
		Generation:  generation,
		Phase:       phase,
		Changes:     changes,
	})
}

// emitStatusTransitionsLocked journals diffs for every runtime entry and
// job. Called from persistLocked after a successful state write — the
// journal is a projection of what actually landed on disk.
func (m *Manager) emitStatusTransitionsLocked(now time.Time) {
	for key, e := range m.items {
		held := m.holds[holdKey(e.binding.PrincipalID, e.binding.ContextID)].Active
		st := runtimeStatusLocked(e, held, now)
		m.emitTransitionsLocked("runtime", key, st.PrincipalID, st.ContextID, st.Generation, st.Phase, st.Conditions)
	}
	for key, record := range m.jobs {
		st := jobStatusLocked(record)
		m.emitTransitionsLocked("job", key, st.PrincipalID, st.ContextID, record.Generation, st.Phase, st.Conditions)
	}
}

func (m *Manager) appendTransitionLocked(t statusTransition) {
	line, err := json.Marshal(t)
	if err != nil {
		return
	}
	path := m.transitionsPath()
	if info, err := os.Stat(path); err == nil && info.Size() >= 4<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 4096)
	_, _ = w.Write(line)
	_ = w.WriteByte('\n')
	_ = w.Flush()
}
