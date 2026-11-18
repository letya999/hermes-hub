package supervisor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func statusEntry(mut func(*runtimeEntry)) *runtimeEntry {
	e := &runtimeEntry{
		Runtime: Runtime{
			PrincipalID:     "alice",
			ContextID:       "ctx",
			RuntimeID:       "rt",
			Generation:      "g1",
			Desired:         true,
			State:           Ready,
			RuntimeHealth:   "running",
			HermesReadiness: "ready",
			Ownership:       "verified",
			ConnectorHealth: "healthy",
		},
		binding: Binding{PrincipalID: "alice", ContextID: "ctx"},
	}
	if mut != nil {
		mut(e)
	}
	return e
}

func TestRuntimeStatusPhaseMapping(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		mut   func(*runtimeEntry)
		held  bool
		phase string
	}{
		{"ready", nil, false, PhaseRunning},
		{"held", nil, true, PhaseSuspended},
		{"stopping", func(e *runtimeEntry) { e.State = Stopping }, false, PhaseTerminating},
		{"undesired running winds down", func(e *runtimeEntry) { e.Desired = false }, false, PhaseTerminating},
		{"undesired stopped", func(e *runtimeEntry) {
			e.Desired = false
			e.RuntimeHealth = "stopped"
		}, false, PhaseStopped},
		{"dead desired", func(e *runtimeEntry) { e.RuntimeHealth = "exited" }, false, PhaseFailed},
		{"inspect unknown", func(e *runtimeEntry) { e.RuntimeHealth = "unknown" }, false, PhaseUnknown},
		{"running not ready", func(e *runtimeEntry) { e.HermesReadiness = "unavailable" }, false, PhaseDegraded},
		{"running degraded readiness", func(e *runtimeEntry) { e.HermesReadiness = "degraded" }, false, PhaseDegraded},
		{"running connectors degraded", func(e *runtimeEntry) { e.ConnectorHealth = "degraded" }, false, PhaseDegraded},
		{"running never probed", func(e *runtimeEntry) {
			e.HermesReadiness = ""
			e.ConnectorHealth = ""
		}, false, PhasePending},
		{"missing desired", func(e *runtimeEntry) { e.RuntimeHealth = "missing" }, false, PhasePending},
		{"crash budget exhausted", func(e *runtimeEntry) {
			e.CrashWindowStart = now.Add(-time.Minute)
			e.CrashCount = 3
		}, false, PhaseFailed},
		{"unrecognized docker state", func(e *runtimeEntry) { e.RuntimeHealth = "removing" }, false, PhasePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := runtimeStatusLocked(statusEntry(tc.mut), tc.held, now)
			if st.Phase != tc.phase {
				t.Fatalf("phase=%q want %q (conditions=%+v)", st.Phase, tc.phase, st.Conditions)
			}
			if len(st.Conditions) != 4 {
				t.Fatalf("conditions=%d want 4", len(st.Conditions))
			}
		})
	}
}

func TestRuntimeStatusConditionsClosed(t *testing.T) {
	now := time.Now()
	// Every condition name and reason must come from the closed vocabulary.
	names := map[string]bool{CondReady: true, CondOwnership: true, CondConnectors: true, CondCrashBudget: true}
	reasons := map[string]bool{
		ReasonReady: true, ReasonReadinessDegraded: true, ReasonReadinessUnavailable: true,
		ReasonNotChecked: true, ReasonVerified: true, ReasonUnverified: true, ReasonAbsent: true,
		ReasonHealthy: true, ReasonDegraded: true, ReasonUnknown: true, ReasonNotConfigured: true,
		ReasonWithinBudget: true, ReasonBudgetExhausted: true, ReasonRetryDeferred: true,
	}
	muts := []func(*runtimeEntry){
		nil,
		func(e *runtimeEntry) { e.HermesReadiness = "garbage-value" },
		func(e *runtimeEntry) { e.Ownership = "??" },
		func(e *runtimeEntry) { e.ConnectorHealth = "unexpected" },
		func(e *runtimeEntry) {
			e.HermesReadiness, e.Ownership, e.ConnectorHealth = "", "", ""
			e.CrashWindowStart = now.Add(-time.Minute)
			e.CrashCount = 9
		},
	}
	for i, mut := range muts {
		st := runtimeStatusLocked(statusEntry(mut), false, now)
		for _, c := range st.Conditions {
			if !names[c.Name] {
				t.Fatalf("case %d: unknown condition name %q", i, c.Name)
			}
			if !reasons[c.Reason] {
				t.Fatalf("case %d: reason %q escaped the closed vocabulary", i, c.Reason)
			}
		}
	}
}

func TestJobStatusProjection(t *testing.T) {
	rec := jobRecord{
		Request:   hubruntime.ExecuteRequest{JobID: "j1", PrincipalID: "alice", ContextID: "ctx"},
		Status:    "running",
		Phase:     phaseRunning,
		UpdatedAt: time.Now(),
	}
	st := jobStatusLocked(rec)
	if st.Phase != phaseRunning {
		t.Fatalf("phase=%q", st.Phase)
	}
	if st.Conditions[0].Name != CondJobAdmitted || st.Conditions[0].OK {
		t.Fatalf("unadmitted job must report admitted=false, got %+v", st.Conditions[0])
	}
	// unconfirmed is a condition, never a phase
	rec.Unconfirmed, rec.UnconfirmedSince = true, time.Now().Add(-time.Minute)
	st = jobStatusLocked(rec)
	if st.Phase != phaseRunning {
		t.Fatalf("unconfirmed must not change phase, got %q", st.Phase)
	}
	outcome := st.Conditions[1]
	if outcome.Name != CondJobOutcome || outcome.OK || outcome.Reason != ReasonUnconfirmed {
		t.Fatalf("outcome condition wrong: %+v", outcome)
	}
	// empty phase collapses to unknown rather than ""
	rec.Phase = ""
	if got := jobStatusLocked(rec).Phase; got != PhaseUnknown {
		t.Fatalf("empty phase=%q want unknown", got)
	}
}

func readTransitions(t *testing.T, path string) []statusTransition {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []statusTransition
	s := bufio.NewScanner(f)
	for s.Scan() {
		var tr statusTransition
		if err := json.Unmarshal(s.Bytes(), &tr); err != nil {
			t.Fatalf("bad journal line: %v", err)
		}
		out = append(out, tr)
	}
	return out
}

func TestTransitionsJournalEmitOnPersist(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		items:     map[string]*runtimeEntry{},
		jobs:      map[string]jobRecord{},
		cfg:       Config{StateDir: dir},
		statePath: filepath.Join(dir, "supervisor.json"),
	}
	m.items["alice:ctx"] = statusEntry(nil)
	m.jobs["j1"] = jobRecord{
		Request:   hubruntime.ExecuteRequest{JobID: "j1", PrincipalID: "alice", ContextID: "ctx"},
		Status:    "running",
		Phase:     phaseRunning,
		UpdatedAt: time.Now(),
	}
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	journal := readTransitions(t, filepath.Join(dir, transitionsFile))
	if len(journal) != 2 {
		t.Fatalf("first persist should baseline both entries, got %d lines", len(journal))
	}
	var rt *statusTransition
	for i := range journal {
		if journal[i].Kind == "runtime" {
			rt = &journal[i]
		}
	}
	if rt == nil || rt.Phase != PhaseRunning {
		t.Fatalf("runtime transition missing or wrong phase: %+v", rt)
	}

	// steady state: no new lines
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if got := len(readTransitions(t, filepath.Join(dir, transitionsFile))); got != 2 {
		t.Fatalf("steady persist emitted again: %d lines", got)
	}

	// a real change journals once, naming only the moved conditions
	m.items["alice:ctx"].HermesReadiness = "unavailable"
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	journal = readTransitions(t, filepath.Join(dir, transitionsFile))
	if len(journal) != 3 {
		t.Fatalf("expected 3 lines after degrade, got %d", len(journal))
	}
	last := journal[2]
	if last.Phase != PhaseDegraded {
		t.Fatalf("degraded emit phase=%q", last.Phase)
	}
	if len(last.Changes) != 1 || last.Changes[0].Name != CondReady {
		t.Fatalf("changes should name only the moved condition: %+v", last.Changes)
	}
}

func TestStatusViewAggregates(t *testing.T) {
	m := &Manager{
		items: map[string]*runtimeEntry{"alice:ctx": statusEntry(nil)},
		jobs: map[string]jobRecord{
			"j-open": {Request: hubruntime.ExecuteRequest{JobID: "j-open", PrincipalID: "alice", ContextID: "ctx"}, Phase: phaseRunning, Status: "running"},
			"j-done": {Request: hubruntime.ExecuteRequest{JobID: "j-done", PrincipalID: "alice", ContextID: "ctx"}, Phase: phaseCompleted, Status: "completed"},
			"j-bad":  {Request: hubruntime.ExecuteRequest{JobID: "j-bad", PrincipalID: "alice", ContextID: "ctx"}, Phase: "", Status: "weird"},
		},
		cfg: Config{StateDir: t.TempDir()},
	}
	view := m.StatusView()
	if len(view.Runtimes) != 1 || view.Runtimes[0].Phase != PhaseRunning {
		t.Fatalf("runtimes=%+v", view.Runtimes)
	}
	if view.Jobs.Total != 3 || view.Jobs.ByPhase[phaseRunning] != 1 || view.Jobs.ByPhase[phaseCompleted] != 1 || view.Jobs.ByPhase[PhaseUnknown] != 1 {
		t.Fatalf("job counts wrong: %+v", view.Jobs)
	}
	if len(view.Jobs.Open) != 2 { // running + unknown are non-terminal
		t.Fatalf("open=%+v", view.Jobs.Open)
	}
	for _, j := range view.Jobs.Open {
		if strings.Contains(j.JobID, "done") {
			t.Fatalf("terminal job leaked into open: %+v", j)
		}
	}
}

// A space that never backed up must not emit the year-1 zero time — a
// negative Unix gauge reads as a real (absurd) timestamp in Prometheus.
func TestMetricsRendersZeroForNeverBackedUp(t *testing.T) {
	m := &Manager{
		cfg:     Config{StateDir: t.TempDir()},
		backups: map[string]BackupRecord{"alice": {User: "alice", DrillResult: "no_archive"}},
	}
	var out strings.Builder
	m.StatusView().Metrics(&out)
	if strings.Contains(out.String(), "-62135596800") {
		t.Fatalf("zero time leaked as a timestamp:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `hub_supervisor_backup_last_success_seconds{user="alice"} 0`) {
		t.Fatalf("never-backed-up user must render 0:\n%s", out.String())
	}
}
