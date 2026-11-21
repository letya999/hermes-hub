package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestReconcileDoesNotBlockAcquire(t *testing.T) {
	probing := make(chan struct{})
	release := make(chan struct{})
	armedCh := make(chan struct{})
	var once sync.Once
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(_ context.Context, _, _ string) error {
		select {
		case <-armedCh:
			once.Do(func() { close(probing) })
			<-release
		default:
		}
		return nil
	})
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	close(armedCh)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ReconcileHealth(context.Background(), time.Now())
	}()
	select {
	case <-probing:
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile never probed")
	}
	acquired := make(chan error, 1)
	go func() {
		lease, _, err := m.Acquire(context.Background(), b, LeaseJob)
		if err == nil {
			_ = m.ReleaseLease(lease.ID)
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("acquire during reconcile probe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire blocked behind reconcile probe")
	}
	close(release)
	<-done
}

func TestHealthReconciliationSeparatesReadinessAndPreservesUnknown(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	unknown := func() { stub.setRenewErr(errors.New("inspection unavailable")) }
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseBinding(b); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), time.Now())
	r, _, err := m.Status(b)
	if err != nil || r.RuntimeHealth != "running" || r.HermesReadiness != "ready" || r.Desired {
		t.Fatalf("status=%+v err=%v", r, err)
	}
	unknown()
	m.ReconcileHealth(context.Background(), time.Now())
	if err := m.Reap(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if stub.releaseCount() != 0 {
		t.Fatal("unknown runtime reaped")
	}
	stub.setRenewErr(nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{PrincipalID: b.PrincipalID, ContextID: b.ContextID}, JobID: "active", IdempotencyKey: "active", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), time.Now())
	if err := m.Reap(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if stub.releaseCount() != 0 {
		t.Fatal("active durable job reaped")
	}
}

func TestCrashBudgetAndBackoffSurviveSupervisorRestart(t *testing.T) {
	stub := &claimStub{err: errors.New("start failed")}
	runs := func() int { return stub.claimCount() }
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub.reader = supTestReader{dir: root}
	m.cfg.Spawn = stub
	now := time.Unix(100, 0)
	m.cfg.Now = func() time.Time { return now }
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err == nil {
		t.Fatal("failed start accepted")
	}
	if _, err := m.Ensure(context.Background(), b); err == nil || runs() != 1 {
		t.Fatal("backoff bypassed")
	}
	var err error
	m, err = newTestManager(t, m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Ensure(context.Background(), b); err == nil || runs() != 1 {
		t.Fatal("restart reset backoff")
	}
	for attempt := 0; attempt < 2; attempt++ {
		now = now.Add(10 * time.Second)
		if _, err = m.Ensure(context.Background(), b); err == nil {
			t.Fatal("failed start accepted")
		}
	}
	now = now.Add(time.Minute)
	if _, err = m.Ensure(context.Background(), b); err == nil || runs() != 3 {
		t.Fatalf("crash budget bypassed: %d", runs())
	}
	r, _, err := m.Status(b)
	if err != nil || r.CrashCount != 3 || r.NextRetryAt.IsZero() {
		t.Fatalf("unobservable crash policy: %+v %v", r, err)
	}
	now = now.Add(10 * time.Minute)
	if _, err = m.Ensure(context.Background(), b); err == nil || runs() != 4 {
		t.Fatalf("new recovery window: %d", runs())
	}
}

func TestRuntimeStartRequiresDurableIntent(t *testing.T) {
	mutations := 0
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { mutations++; return nil, nil }, func(context.Context, string, string) error { return nil })
	m.statePath = root // An existing directory makes the atomic state rename fail.
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil {
		t.Fatal("start without durable state accepted")
	}
	if mutations != 0 || len(m.sem) != 0 {
		t.Fatalf("external mutation or leaked slot: %d %d", mutations, len(m.sem))
	}
}

func TestMissingRuntimeRecoveryUsesCurrentWorkWithoutResubmission(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active"}[active], func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			stub := m.cfg.Spawn.(*claimStub)
			runs := stub.claimCount
			b := binding(root)
			clockNow := time.Unix(100, 0)
			m.cfg.Now = func() time.Time { return clockNow }
			runtime, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "recover", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "recover", Text: "private input"}
			if active {
				if _, _, err = m.beginJob(request); err != nil {
					t.Fatal(err)
				}
				m.bindJobGeneration(request, runtime.Generation)
				outcome := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "uncertain"}
				if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
					t.Fatal(err)
				}
			}
			stub.setRenewErr(spawnv1.ErrNotFound)
			m.ReconcileHealth(context.Background(), time.Now())
			status, _, err := m.Status(b)
			if err != nil || status.RuntimeHealth != "missing" {
				t.Fatalf("absence not authoritative: %+v %v", status, err)
			}
			m.RecoverMissingWork(context.Background())
			if runs() != 1 {
				t.Fatal("replacement skipped backoff")
			}
			clockNow = clockNow.Add(10 * time.Second)
			var workers sync.WaitGroup
			for i := 0; i < 20; i++ {
				workers.Add(1)
				go func() { defer workers.Done(); m.RecoverMissingWork(context.Background()) }()
			}
			workers.Wait()
			m.RecoverMissingWork(context.Background())
			expected := 1
			if active {
				expected = 2
			}
			if runs() != expected {
				t.Fatalf("runs=%d expected=%d", runs(), expected)
			}
			if active {
				status, _, _ = m.Status(b)
				if status.Generation == runtime.Generation || len(status.Consumers) != 1 {
					t.Fatalf("replacement=%+v", status)
				}
				m.mu.Lock()
				record := m.jobs[request.JobID]
				m.mu.Unlock()
				if record.Response.RunID != "original" || record.Request.Text != "" {
					t.Fatalf("resubmitted or changed original: %+v", record)
				}
				for loss := 2; loss <= 3; loss++ {
					stub.setRenewErr(spawnv1.ErrNotFound)
					m.ReconcileHealth(context.Background(), clockNow)
					m.ReconcileHealth(context.Background(), clockNow)
					status, _, _ = m.Status(b)
					if status.CrashCount != loss {
						t.Fatalf("same loss counted twice: %+v", status)
					}
					clockNow = clockNow.Add(10 * time.Second)
					m.RecoverMissingWork(context.Background())
					if runs() != 3 {
						t.Fatalf("replacement budget loss=%d runs=%d", loss, runs())
					}
				}
			} else {
				status, _, _ = m.Status(b)
				if status.State != Stopped || len(status.Consumers) != 0 {
					t.Fatalf("missing idle runtime stayed registered: %+v", status)
				}
				if _, err := m.Ensure(context.Background(), b); err != nil || runs() != 2 {
					t.Fatalf("idle wake after disappearance: runs=%d err=%v", runs(), err)
				}
			}
		})
	}
}

// A claimed runtime's "exited" state is its row forgetting the lease:
// the sweep reads missing, and only desired work (a live job, never an
// idle entry) earns a fresh claim. Foreign-container ownership is
// spawn-controller's lane now — its label reclaim is covered there.
func TestExitedRuntimeRecoveryRequiresWorkAndOwnership(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		active bool
	}{{"active", true}, {"idle", false}} {
		t.Run(scenario.name, func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			stub := m.cfg.Spawn.(*claimStub)
			clockNow := time.Unix(100, 0)
			m.cfg.Now = func() time.Time { return clockNow }
			b := binding(root)
			runtime, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "exited", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "exited", Text: "private"}
			if scenario.active {
				if _, _, err = m.beginJob(request); err != nil {
					t.Fatal(err)
				}
				m.bindJobGeneration(request, runtime.Generation)
				outcome := hubruntime.ExecuteResponse{JobID: request.JobID, RunID: "original", SessionID: "session", RuntimeGeneration: runtime.Generation, Status: "uncertain"}
				if err = m.finishJobGeneration(request, outcome, outcome.Status, runtime.Generation); err != nil {
					t.Fatal(err)
				}
			}
			stub.setRenewErr(spawnv1.ErrNotFound)
			m.ReconcileHealth(context.Background(), clockNow)
			clockNow = clockNow.Add(10 * time.Second)
			m.RecoverMissingWork(context.Background())
			m.RecoverMissingWork(context.Background())
			if scenario.active {
				if stub.claimCount() != 2 {
					t.Fatalf("replacement claims=%d", stub.claimCount())
				}
				m.mu.Lock()
				record := m.jobs[request.JobID]
				m.mu.Unlock()
				if record.Response.RunID != "original" {
					t.Fatal("original run replaced")
				}
			} else if stub.claimCount() != 1 {
				t.Fatalf("unneeded replacement claims=%d", stub.claimCount())
			}
		})
	}
}

func TestRuntimeHealthContractChecksAuthenticationBoundsAndStatuses(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	health := hubruntime.RuntimeHealth{HermesReadiness: "ready", ConnectorHealth: "degraded", Degraded: 1, ExternalConnections: "not_probed"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Header.Get("Authorization") != "Bearer context-secret" {
			t.Error("health bypassed private auth")
		}
		_ = json.NewEncoder(w).Encode(health)
	}))
	defer api.Close()
	observed, err := m.runtimeHealth(context.Background(), api.URL, "context-secret")
	if err != nil || observed.HermesReadiness != "ready" || observed.ConnectorHealth != "degraded" {
		t.Fatalf("separate health: %+v %v", observed, err)
	}
	for _, kind := range []string{"readiness", "connector", "count", "external"} {
		original := health
		switch kind {
		case "readiness":
			health.HermesReadiness = "secret-text"
		case "connector":
			health.ConnectorHealth = "secret-text"
		case "count":
			health.Unknown = 65
		case "external":
			health.ExternalConnections = "healthy"
		}
		if _, err := m.runtimeHealth(context.Background(), api.URL, "context-secret"); err == nil {
			t.Fatalf("invalid %s accepted", kind)
		}
		health = original
	}
}

// SPEC-0053 R1.4-R1.6: the supervisor accepts the additive tool-surface and
// model fields a current runtime reports, and still rejects out-of-vocabulary
// or oversized values so a hostile or broken runtime cannot poison the record.
func TestRuntimeHealthAcceptsToolSurfaceFields(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	health := hubruntime.RuntimeHealth{
		HermesReadiness: "degraded", ConnectorHealth: "healthy", Connected: 1, ExternalConnections: "not_probed",
		ToolSurface: "degraded", MissingTools: []string{"image_gen", "terminal"},
		ToolReasons: map[string]string{"hub": "child_env_unresolved:HUB_POLICY_VERSION"}, ModelStatus: "model_not_found",
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(health)
	}))
	defer api.Close()
	observed, err := m.runtimeHealth(context.Background(), api.URL, "context-secret")
	if err != nil || observed.ToolSurface != "degraded" || observed.ModelStatus != "model_not_found" || len(observed.MissingTools) != 2 {
		t.Fatalf("extended health rejected: %+v %v", observed, err)
	}
	for _, kind := range []string{"surface", "model", "missing", "reasons"} {
		original := health
		switch kind {
		case "surface":
			health.ToolSurface = "rm -rf"
		case "model":
			health.ModelStatus = "secret"
		case "missing":
			health.MissingTools = make([]string, 65)
		case "reasons":
			health.ToolReasons = map[string]string{"hub": strings.Repeat("x", 1025)}
		}
		if _, err := m.runtimeHealth(context.Background(), api.URL, "context-secret"); err == nil {
			t.Fatalf("invalid %s accepted", kind)
		}
		health = original
	}
}

// SPEC-0053 R1.4: the reconciler persists the runtime's tool-surface and model
// observations on the durable runtime record so /v1/runtimes (and /status via
// the gateway) can show them without re-probing the runtime.
func TestReconcilePersistsToolSurface(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{
			HermesReadiness: "degraded", ConnectorHealth: "healthy", Connected: 1, ExternalConnections: "not_probed",
			ToolSurface: "degraded", MissingTools: []string{"image_gen"},
			ToolReasons: map[string]string{"hub": "child_env_unresolved:HUB_POLICY_VERSION"}, ModelStatus: "model_not_found",
		})
	}))
	defer api.Close()
	m.cfg.Probe = nil
	key := runtimeKey(b)
	m.mu.Lock()
	m.items[key].Runtime.Address = api.URL
	m.mu.Unlock()
	m.ReconcileHealth(context.Background(), time.Now())
	r, _, err := m.Status(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolSurface != "degraded" || r.ModelStatus != "model_not_found" || len(r.MissingTools) != 1 || r.MissingTools[0] != "image_gen" {
		t.Fatalf("surface not persisted: %+v", r)
	}
	if r.ToolReasons["hub"] != "child_env_unresolved:HUB_POLICY_VERSION" {
		t.Fatalf("reasons not persisted: %+v", r.ToolReasons)
	}
}

// SPEC-0053 R1.4: a degraded tool surface is observation, never a restart
// cause. Remediation stays gated on readiness "unavailable" alone, so the
// reconciler must not issue docker restart for a degraded-only runtime even
// when every other wedge condition is met.
func TestToolSurfaceDegradedNeverRestarts(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	restarts := stub.drainCount
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseBinding(b); err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{PrincipalID: b.PrincipalID, ContextID: b.ContextID}, JobID: "active", IdempotencyKey: "active", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{
			HermesReadiness: "degraded", ConnectorHealth: "healthy", Connected: 1, ExternalConnections: "not_probed",
			ToolSurface: "degraded", MissingTools: []string{"terminal"}, ModelStatus: "model_not_found",
		})
	}))
	defer api.Close()
	m.cfg.Probe = nil
	key := runtimeKey(b)
	m.mu.Lock()
	m.items[key].Runtime.Address = api.URL
	m.items[key].consecutiveUncertain = 5
	m.items[key].readinessBadSince = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.ReconcileHealth(context.Background(), time.Now())
	if restarts() != 0 {
		t.Fatal("degraded tool surface triggered a restart")
	}
}

func TestIdleMissingReapReleasesCapacityOnlyAfterDurableHandoff(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable", true: "write-failure"}[failWrite], func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return nil, nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			if _, err := m.Ensure(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			if err := m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			mutations := 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "inspect":
					return nil, os.ErrNotExist
				case "ps":
					return nil, nil
				default:
					mutations++
					return nil, nil
				}
			}
			if failWrite {
				m.statePath = root
			}
			err := m.Reap(context.Background(), time.Now().Add(time.Hour))
			status, _, _ := m.Status(b)
			if mutations != 0 {
				t.Fatal("absence caused external mutation")
			}
			if failWrite {
				if err == nil || status.State != Idle || len(m.sem) != 1 {
					t.Fatalf("unsafe failed handoff: %+v %v", status, err)
				}
			} else if err != nil || status.State != Stopped || len(m.sem) != 0 {
				t.Fatalf("capacity retained: %+v %v", status, err)
			}
		})
	}
}

// A claimed runtime survives a supervisor restart through its row: the
// restore renews the held lease instead of re-claiming, so the same
// generation keeps serving and the opaque lifecycle lease stays intact.
func TestLifecycleLeaseRecoversOnceAndRetainsOpaqueReleaseAcrossRestart(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	b := binding(root)
	lease, first, err := m.Acquire(context.Background(), b, LeaseLifecycle)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	m, err = newTestManager(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), time.Unix(100, 0))
	m.cfg.Now = func() time.Time { return time.Unix(110, 0) }
	m.RecoverMissingWork(context.Background())
	m.RecoverMissingWork(context.Background())
	current, _, err := m.Status(b)
	if err != nil || stub.claimCount() != 1 || current.Generation != first.Generation || len(current.Consumers) != 1 {
		t.Fatalf("lifecycle restore=%+v claims=%d err=%v", current, stub.claimCount(), err)
	}
	if err := m.ReleaseLease(lease.ID); err != nil {
		t.Fatal(err)
	}
	current, _, _ = m.Status(b)
	if len(current.Consumers) != 0 || current.State != Idle {
		t.Fatal("original opaque lifecycle release lost")
	}
}

func TestWedgedRuntimeRestartRemediatesConsecutiveUncertainJobs(t *testing.T) {
	readiness := "unavailable"
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	restarts := stub.drainCount
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: readiness, ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
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
	if err := m.ReleaseBinding(b); err != nil {
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
	now := time.Unix(200, 0)
	outcome("wedged-1")
	m.ReconcileHealth(context.Background(), now)
	outcome("wedged-2")
	m.ReconcileHealth(context.Background(), now)
	if restarts() != 0 {
		t.Fatal("remediated before the readiness evidence window")
	}
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow))
	if restarts() != 1 {
		t.Fatalf("wedged runtime not remediated: %d", restarts())
	}
	outcome("wedged-3")
	outcome("wedged-4")
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow+time.Second))
	if restarts() != 1 {
		t.Fatalf("cooldown bypassed: %d", restarts())
	}
}

// A wedged runtime that still holds a live lease has a job, stream, approval
// or lifecycle in flight — restarting it would kill that work, so remediation
// defers until the runtime is free. A claimed runtime restarts by draining
// its row; the stop grace is spawn-controller's backend budget now.
func TestWedgedRemediationSkipsLeasedRuntimeAndUsesSigtermGrace(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
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
	if err := m.ReleaseBinding(b); err != nil {
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
	now := time.Unix(200, 0)
	outcome("lease-wedged-1")
	outcome("lease-wedged-2")
	lease, _, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), now)
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow))
	if stub.drainCount() != 0 {
		t.Fatalf("leased runtime restarted mid-work: %d", stub.drainCount())
	}
	if err := m.ReleaseLease(lease.ID); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow+time.Second))
	if stub.drainCount() != 1 {
		t.Fatalf("remediation drain missing: %d", stub.drainCount())
	}
}

// A degraded runtime is still serving (e.g. host disk above the readiness
// threshold); restarting it kills in-flight runs and cannot remediate a
// host-side cause. Only an unavailable runtime may be restarted.
func TestDegradedRuntimeDoesNotRemediateUncertainJobs(t *testing.T) {
	readiness := "degraded"
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	restarts := stub.drainCount
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: readiness, ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
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
	now := time.Unix(200, 0)
	for _, id := range []string{"degraded-1", "degraded-2", "degraded-3", "degraded-4"} {
		outcome(id)
	}
	m.ReconcileHealth(context.Background(), now)
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow))
	m.ReconcileHealth(context.Background(), now.Add(2*wedgedReadinessWindow))
	if restarts() != 0 {
		t.Fatalf("degraded runtime restarted %d times; degraded readiness is not restart-remediable", restarts())
	}
}

func TestSingleUncertainJobDoesNotRemediate(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	restarts := stub.drainCount
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(hubruntime.RuntimeHealth{HermesReadiness: "degraded", ConnectorHealth: "not_configured", ExternalConnections: "not_probed"})
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
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "single", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "single", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	response := hubruntime.ExecuteResponse{JobID: "single", RuntimeGeneration: runtime.Generation, Status: "uncertain"}
	if err := m.finishJobGeneration(request, response, "uncertain", runtime.Generation); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0)
	m.ReconcileHealth(context.Background(), now)
	m.ReconcileHealth(context.Background(), now.Add(wedgedReadinessWindow))
	m.ReconcileHealth(context.Background(), now.Add(2*wedgedReadinessWindow))
	if restarts() != 0 {
		t.Fatalf("single uncertain job remediated a runtime: %d", restarts())
	}
}

func TestDockerCommandDeadlineBoundsBackgroundSweeps(t *testing.T) {
	m, _ := testManager(t, func(ctx context.Context, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 31*time.Second {
			t.Fatal("background Docker operation has no finite deadline")
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	if _, err := m.command(context.Background(), "ps"); err != nil {
		t.Fatal(err)
	}
}

// A spawn parked on the capacity semaphore holds the runtime key lock; the
// health sweep must skip that entry instead of wedging the whole watchdog.
func TestReconcileHealthSkipsHeldRuntimeLock(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	lock := m.lockFor(runtimeKey(b))
	lock.Lock()
	defer lock.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ReconcileHealth(context.Background(), time.Now())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile wedged behind a held runtime lock")
	}
}

// The post-probe re-lock can race a fresh spawn hold the same way: the entry
// update is skipped for this tick rather than stalling the sweep.
func TestReconcileHealthSkipsHeldRuntimeLockOnUpdate(t *testing.T) {
	probing := make(chan struct{})
	release := make(chan struct{})
	armed := make(chan struct{})
	var once sync.Once
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(_ context.Context, _, _ string) error {
		select {
		case <-armed:
			once.Do(func() { close(probing) })
			<-release
		default:
		}
		return nil
	})
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	close(armed)
	lock := m.lockFor(runtimeKey(b))
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ReconcileHealth(context.Background(), time.Now())
	}()
	select {
	case <-probing:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile never probed")
	}
	lock.Lock()
	defer lock.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile wedged behind a held runtime lock on update")
	}
}

// The same held key lock must not wedge the idle reaper: capacity behind a
// parked spawn still drains on the next pass.
func TestReapSkipsHeldRuntimeLock(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseBinding(b); err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(b)
	m.mu.Lock()
	entry := m.items[key]
	entry.IdleDeadline = m.cfg.Now().Add(-time.Minute)
	entry.RuntimeHealth = "running"
	entry.Ownership = "verified"
	m.mu.Unlock()
	lock := m.lockFor(key)
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.Reap(context.Background(), m.cfg.Now())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		lock.Unlock()
		t.Fatal("reap wedged behind a held runtime lock")
	}
	m.mu.Lock()
	state := m.items[key].State
	m.mu.Unlock()
	if state != Idle {
		lock.Unlock()
		t.Fatalf("held entry mutated during skip: %s", state)
	}
	lock.Unlock()
	if err := m.Reap(context.Background(), m.cfg.Now()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.items[key].State; state != Stopped {
		t.Fatalf("released entry was not reaped: %s", state)
	}
}

// Orphan cleanup runs inside the sweep before health and reap: a key lock
// held by a parked spawn must not wedge orphan collection either.
func TestReapOrphansSkipsHeldRuntimeLock(t *testing.T) {
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
	key := runtimeKey(b)
	request := hubruntime.ExecuteRequest{PrincipalID: b.PrincipalID, ContextID: b.ContextID}
	m.mu.Lock()
	m.jobs["job-orphan"] = jobRecord{Request: request, Generation: "gen-old", Status: "completed"}
	m.orphans = []OrphanRuntime{{Container: "deadbeef12", Name: "orphan-x", ContextKey: hex.EncodeToString(hashBytes(key)), Generation: "gen-old"}}
	m.mu.Unlock()
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ReapOrphans(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("orphan reap wedged behind a held runtime lock")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.orphans) != 1 {
		t.Fatalf("orphan unexpectedly removed under a held lock (generation %s)", runtime.Generation)
	}
}
