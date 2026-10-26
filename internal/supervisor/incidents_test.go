package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// A runtime parked on an approval deadline inside its window is expected
// waiting, not a wedge: the watchdog names the wait in status, never
// remediates and never opens an incident for it. Only when the approval's
// hold is gone does the same unhealthy evidence become wedged work.
func TestWatchdogExpectedWait(t *testing.T) {
	restarts := 0
	started := false
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && !started {
			return nil, os.ErrNotExist
		}
		if args[0] == "run" {
			started = true
			return []byte("container"), nil
		}
		if args[0] == "inspect" {
			return []byte("running"), nil
		}
		if args[0] == "restart" {
			restarts++
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: "unavailable", ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	b := binding(root)
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, entry := range m.items {
		entry.Address = api.URL
	}
	m.mu.Unlock()
	m.cfg.Probe = nil
	outcome := func(id string) {
		request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: id, Text: "work"}
		if _, _, err := m.beginJob(request); err != nil {
			t.Fatal(err)
		}
		if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
			t.Fatal(err)
		}
		response := hubruntime.ExecuteResponse{JobID: id, RuntimeGeneration: runtime.Generation, Status: "uncertain"}
		if err := m.finishJobGeneration(request, response, "uncertain", runtime.Generation); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(300, 0)
	outcome("approval-wait-1")
	outcome("approval-wait-2")
	if _, _, err := m.Acquire(context.Background(), b, LeaseApproval); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow+time.Minute))
	if restarts != 0 {
		t.Fatalf("parked approval restarted: %d", restarts)
	}
	status, ok, err := m.Status(b)
	if err != nil || !ok {
		t.Fatalf("status ok=%v err=%v", ok, err)
	}
	if status.WaitReason != "approval" {
		t.Fatalf("wait reason=%q, want approval", status.WaitReason)
	}
	m.mu.Lock()
	incidents := len(m.items[runtimeKey(b)].incidents)
	m.mu.Unlock()
	if incidents != 0 {
		t.Fatalf("expected waiting opened %d incidents", incidents)
	}
	if blob, err := os.ReadFile(m.signalPath()); err == nil && len(blob) != 0 {
		t.Fatalf("expected wait wrote signals: %s", blob)
	}
}

// A wedged runtime produces one deduplicated, durable incident — not one per
// sweep: repeated escalation evidence updates the same record instead of
// appending noise, and the incident survives a supervisor restart alongside
// the runtime it belongs to.
func TestWatchdogIncidentCoalescing(t *testing.T) {
	restarts := 0
	started := false
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && !started {
			return nil, os.ErrNotExist
		}
		if args[0] == "run" {
			started = true
			return []byte("container"), nil
		}
		if args[0] == "inspect" {
			return []byte("running"), nil
		}
		if args[0] == "restart" {
			restarts++
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: "unavailable", ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	b := binding(root)
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, entry := range m.items {
		entry.Address = api.URL
	}
	m.mu.Unlock()
	m.cfg.Probe = nil
	outcome := func(id string) {
		request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: id, Text: "work"}
		if _, _, err := m.beginJob(request); err != nil {
			t.Fatal(err)
		}
		if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
			t.Fatal(err)
		}
		response := hubruntime.ExecuteResponse{JobID: id, RuntimeGeneration: runtime.Generation, Status: "uncertain"}
		if err := m.finishJobGeneration(request, response, "uncertain", runtime.Generation); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(400, 0)
	outcome("coalesce-1")
	outcome("coalesce-2")
	m.ReconcileHealth(context.Background(), now)
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow))
	if restarts != 1 {
		t.Fatalf("wedged runtime not remediated: %d", restarts)
	}
	m.mu.Lock()
	incident, ok := m.items[runtimeKey(b)].incidents["wedged"]
	m.mu.Unlock()
	if !ok || incident.Count != 1 {
		t.Fatalf("incidents=%+v", m.items[runtimeKey(b)].incidents)
	}
	lines := func() int {
		blob, err := os.ReadFile(m.signalPath())
		if err != nil {
			return 0
		}
		text := strings.TrimSpace(string(blob))
		if text == "" {
			return 0
		}
		return strings.Count(text, "\n") + 1
	}
	if got := lines(); got != 1 {
		t.Fatalf("signals=%d, want 1", got)
	}
	// The same evidence observed again during the cooldown updates the open
	// incident instead of opening a second one or appending another signal.
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow+time.Minute))
	m.mu.Lock()
	incident = m.items[runtimeKey(b)].incidents["wedged"]
	total := len(m.items[runtimeKey(b)].incidents)
	m.mu.Unlock()
	if total != 1 || incident.LastAt.IsZero() {
		t.Fatalf("incident dedup broken: %+v", incident)
	}
	if got := lines(); got != 1 {
		t.Fatalf("repeat observation appended signal noise: %d", got)
	}
	restored, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	persisted, ok := restored.items[runtimeKey(b)].incidents["wedged"]
	restored.mu.Unlock()
	if !ok || persisted.Count != incident.Count {
		t.Fatalf("incident lost across restart: %+v", persisted)
	}
}

// A managed run whose outcome never confirmed inside its budget ends as one
// bounded timeout incident on its runtime — exactly one durable record and
// one signal line, regardless of how often the sweep sees it afterwards.
func TestManagedTimeoutOneIncident(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "timeout-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "timeout", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.Status, record.Phase, record.Unconfirmed = "uncertain", phaseRunning, true
	record.UnconfirmedSince = m.cfg.Now().Add(-2 * hubruntime.RunBudget())
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), m.cfg.Now())
	m.reconcileUnconfirmed(context.Background(), m.cfg.Now())
	m.mu.Lock()
	incident, ok := m.items[runtimeKey(b)].incidents["timeout"]
	total := len(m.items[runtimeKey(b)].incidents)
	m.mu.Unlock()
	if !ok || incident.Count != 1 || total != 1 {
		t.Fatalf("timeout incidents=%+v total=%d", incident, total)
	}
	blob, err := os.ReadFile(m.signalPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(blob)), "\n") + 1; got != 1 || !strings.Contains(string(blob), `"class":"timeout"`) {
		t.Fatalf("timeout signals=%q", blob)
	}
}

// An outcome that never confirmed inside its run budget is named failed —
// but nothing about the runtime was verified healthy in doing so. The wedge
// budget counts timeouts as unconfirmed outcomes; otherwise an
// unresponsive-but-running runtime could never reach remediation, because
// the timeout itself would zero the counter that gated it.
func TestTimeoutCountsTowardWedgeBudget(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "timeout-wedge", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "timeout-wedge", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	record.Status, record.Phase, record.Unconfirmed = "uncertain", phaseRunning, true
	record.UnconfirmedSince = m.cfg.Now().Add(-2 * hubruntime.RunBudget())
	m.jobs[request.JobID] = record
	m.mu.Unlock()
	m.reconcileUnconfirmed(context.Background(), m.cfg.Now())
	m.mu.Lock()
	got := m.items[runtimeKey(b)].consecutiveUncertain
	m.mu.Unlock()
	if got < 1 {
		t.Fatalf("timeout naming reset the wedge budget: consecutiveUncertain=%d", got)
	}
}

// The watchdog samples host resource counters once per sweep for every owned
// runtime — a single bounded call, never a per-container stream — and exposes
// its own overhead as metrics so supervision cost stays measurable.
func TestWatchdogStatsAndSweepMetrics(t *testing.T) {
	statsCalls := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if m.cfg.Stats == nil {
		t.Fatal("default stats sampler not wired")
	}
	m.cfg.Stats = dockerStatsCommand(func(_ context.Context, args ...string) ([]byte, error) {
		statsCalls++
		if args[0] != "stats" || len(args) < 5 {
			return nil, errors.New("unexpected stats call")
		}
		var lines []string
		for _, name := range args[4:] {
			lines = append(lines, `{"Name":"`+name+`","CPUPerc":"12.50%","MemUsage":"256MiB / 2GiB"}`)
		}
		lines = append(lines, `{"Name":"foreign","CPUPerc":"90%","MemUsage":"1.5GiB / 4GiB"}`)
		return []byte(strings.Join(lines, "\n")), nil
	})
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if statsCalls != 2 {
		t.Fatalf("stats calls=%d, want one per sweep", statsCalls)
	}
	m.mu.Lock()
	runtime := m.items[runtimeKey(b)].Runtime
	m.mu.Unlock()
	if runtime.CPUPercent != 12.5 || runtime.MemBytes != 268435456 {
		t.Fatalf("stats not recorded: %+v", runtime)
	}
	metrics := m.WatchdogMetrics()
	if metrics.Sweeps != 2 || metrics.Entries != 1 || metrics.LastSweepAt.IsZero() {
		t.Fatalf("watchdog metrics=%+v", metrics)
	}
	sampled, err := m.cfg.Stats(context.Background(), []string{"owned"})
	if err != nil || sampled["foreign"].MemBytes != 1610612736 {
		t.Fatalf("sampler dropped GiB units: %+v %v", sampled, err)
	}
	// Stats failures are observations, not authority: a broken sampler leaves
	// the runtime untouched and simply records nothing.
	m.cfg.Stats = func(context.Context, []string) (map[string]ContainerStat, error) {
		return nil, errors.New("stats unavailable")
	}
	m.ReconcileHealth(context.Background(), m.cfg.Now())
	if metrics.Sweeps != 2 {
		t.Fatal("metrics value moved unexpectedly")
	}
	if got := m.WatchdogMetrics().Sweeps; got != 3 {
		t.Fatalf("sweeps=%d, want 3", got)
	}
}
