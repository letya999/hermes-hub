package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestHealthReconciliationSeparatesReadinessAndPreservesUnknown(t *testing.T) {
	unknown := false
	removed := 0
	started := false
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			started = true
		}
		if args[0] == "inspect" && !started {
			return nil, os.ErrNotExist
		}
		if args[0] == "inspect" && unknown {
			return nil, errors.New("inspection unavailable")
		}
		if args[0] == "rm" {
			removed++
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
	m.ReconcileHealth(context.Background(), time.Now())
	r, _, err := m.Status(b)
	if err != nil || r.RuntimeHealth != "running" || r.HermesReadiness != "ready" || r.Desired {
		t.Fatalf("status=%+v err=%v", r, err)
	}
	unknown = true
	m.ReconcileHealth(context.Background(), time.Now())
	if err := m.Reap(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatal("unknown runtime reaped")
	}
	unknown = false
	request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{PrincipalID: b.PrincipalID, ContextID: b.ContextID}, JobID: "active", IdempotencyKey: "active", Text: "work"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), time.Now())
	if err := m.Reap(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatal("active durable job reaped")
	}
}

func TestCrashBudgetAndBackoffSurviveSupervisorRestart(t *testing.T) {
	runs := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "run" {
			runs++
			return nil, errors.New("start failed")
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	now := time.Unix(100, 0)
	m.cfg.Now = func() time.Time { return now }
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err == nil {
		t.Fatal("failed start accepted")
	}
	if _, err := m.Ensure(context.Background(), b); err == nil || runs != 1 {
		t.Fatal("backoff bypassed")
	}
	var err error
	m, err = New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Ensure(context.Background(), b); err == nil || runs != 1 {
		t.Fatal("restart reset backoff")
	}
	for attempt := 0; attempt < 2; attempt++ {
		now = now.Add(10 * time.Second)
		if _, err = m.Ensure(context.Background(), b); err == nil {
			t.Fatal("failed start accepted")
		}
	}
	now = now.Add(time.Minute)
	if _, err = m.Ensure(context.Background(), b); err == nil || runs != 3 {
		t.Fatalf("crash budget bypassed: %d", runs)
	}
	r, _, err := m.Status(b)
	if err != nil || r.CrashCount != 3 || r.NextRetryAt.IsZero() {
		t.Fatalf("unobservable crash policy: %+v %v", r, err)
	}
	now = now.Add(10 * time.Minute)
	if _, err = m.Ensure(context.Background(), b); err == nil || runs != 4 {
		t.Fatalf("new recovery window: %d", runs)
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
			present := false
			runs := 0
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "inspect":
					if !present {
						return nil, os.ErrNotExist
					}
					return []byte("running"), nil
				case "ps":
					return nil, nil
				case "run":
					runs++
					present = true
					return []byte("container"), nil
				}
				return nil, nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			clockNow := time.Unix(100, 0)
			m.cfg.Now = func() time.Time { return clockNow }
			command := m.cfg.Command
			m.cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" && !present {
					return nil, os.ErrNotExist
				}
				return command(ctx, args...)
			}
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
			present = false
			m.ReconcileHealth(context.Background(), time.Now())
			status, _, err := m.Status(b)
			if err != nil || status.RuntimeHealth != "missing" {
				t.Fatalf("absence not authoritative: %+v %v", status, err)
			}
			m.RecoverMissingWork(context.Background())
			if runs != 1 {
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
			if runs != expected {
				t.Fatalf("runs=%d expected=%d", runs, expected)
			}
			if active {
				status, _, _ = m.Status(b)
				if status.Generation == runtime.Generation || status.Leases != 1 {
					t.Fatalf("replacement=%+v", status)
				}
				m.mu.Lock()
				record := m.jobs[request.JobID]
				m.mu.Unlock()
				if record.Response.RunID != "original" || record.Request.Text != "" {
					t.Fatalf("resubmitted or changed original: %+v", record)
				}
				for loss := 2; loss <= 3; loss++ {
					present = false
					m.ReconcileHealth(context.Background(), clockNow)
					m.ReconcileHealth(context.Background(), clockNow)
					status, _, _ = m.Status(b)
					if status.CrashCount != loss {
						t.Fatalf("same loss counted twice: %+v", status)
					}
					clockNow = clockNow.Add(10 * time.Second)
					m.RecoverMissingWork(context.Background())
					if runs != 3 {
						t.Fatalf("replacement budget loss=%d runs=%d", loss, runs)
					}
				}
			}
		})
	}
}

func TestExitedRuntimeRecoveryRequiresWorkAndOwnership(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		active, foreign bool
	}{{"active", true, false}, {"idle", false, false}, {"foreign", true, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			state := "missing"
			runs, removed := 0, 0
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "inspect":
					if state == "missing" {
						return nil, os.ErrNotExist
					}
					return []byte(state), nil
				case "run":
					runs++
					state = "running"
				case "rm":
					removed++
					if len(args[2]) != 64 {
						t.Error("removed by name")
					}
					state = "missing"
				}
				return nil, nil
			}, func(context.Context, string, string) error { return nil })
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
			if scenario.foreign {
				command := m.cfg.Command
				m.cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
					if args[0] == "inspect" && args[2] == "{{json .Config.Labels}}" {
						return []byte(`{"hermes-hub.owner":"foreign"}`), nil
					}
					return command(ctx, args...)
				}
			}
			state = "exited"
			m.ReconcileHealth(context.Background(), clockNow)
			clockNow = clockNow.Add(10 * time.Second)
			m.RecoverMissingWork(context.Background())
			m.RecoverMissingWork(context.Background())
			if scenario.active && !scenario.foreign {
				if runs != 2 || removed != 1 {
					t.Fatalf("replacement runs=%d removed=%d", runs, removed)
				}
				m.mu.Lock()
				record := m.jobs[request.JobID]
				m.mu.Unlock()
				if record.Response.RunID != "original" {
					t.Fatal("original run replaced")
				}
			} else if runs != 1 || removed != 0 {
				t.Fatalf("unneeded/foreign replacement runs=%d removed=%d", runs, removed)
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

func TestLifecycleLeaseRecoversOnceAndRetainsOpaqueReleaseAcrossRestart(t *testing.T) {
	starts := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "run" {
			starts++
		}
		return nil, nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, first, err := m.Acquire(context.Background(), b, LeaseLifecycle)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	command := cfg.Command
	cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return command(ctx, args...)
	}
	m, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(context.Background(), time.Unix(100, 0))
	m.cfg.Now = func() time.Time { return time.Unix(110, 0) }
	m.RecoverMissingWork(context.Background())
	m.RecoverMissingWork(context.Background())
	current, _, err := m.Status(b)
	if err != nil || starts != 2 || current.Generation == first.Generation || current.Leases != 1 {
		t.Fatalf("lifecycle recovery=%+v starts=%d err=%v", current, starts, err)
	}
	if err := m.ReleaseLease(lease.ID); err != nil {
		t.Fatal(err)
	}
	current, _, _ = m.Status(b)
	if current.Leases != 0 || current.State != Idle {
		t.Fatal("original opaque lifecycle release lost")
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
