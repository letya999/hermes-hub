package supervisor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProjectionRestartBlocksNewLeaseUntilActualReadiness(t *testing.T) {
	var restarting atomic.Bool
	probeEntered := make(chan struct{})
	ready := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/restart" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong restart authority")
		}
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	}))
	defer api.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "restart" {
			id, err := hex.DecodeString(args[len(args)-1])
			if err != nil || len(id) != 32 {
				t.Error("restart did not use immutable owned ID")
			}
			restarting.Store(true)
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error {
		if restarting.Load() {
			close(probeEntered)
			<-ready
		}
		return nil
	})
	owner := binding(root)
	rt, err := m.Ensure(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(owner)
	m.mu.Lock()
	entry := m.items[key]
	entry.Address, entry.Leases, entry.State = api.URL, 0, Idle
	rt = entry.Runtime
	m.mu.Unlock()
	finished := make(chan error, 1)
	go func() { _, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 0); finished <- err }()
	select {
	case <-probeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual daemon restart/readiness not reached")
	}
	acquired := make(chan error, 1)
	go func() { _, _, err := m.Acquire(context.Background(), owner, LeaseJob); acquired <- err }()
	select {
	case err := <-acquired:
		t.Fatal("new lease crossed unfinished restart", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(ready)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
}

// docker restart defaults to a 10 s grace; long runs need SIGTERM room, so the
// supervisor must pass an explicit --time >= 30.
func TestRuntimeRestartUsesSigtermGrace(t *testing.T) {
	var restartArgs []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	}))
	defer api.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "restart" {
			restartArgs = append([]string{}, args...)
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address, entry.Leases, entry.State = api.URL, 0, Idle
	rt := entry.Runtime
	m.mu.Unlock()
	applied, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 0)
	if err != nil || !applied {
		t.Fatal(applied, err)
	}
	if len(restartArgs) != 4 || restartArgs[0] != "restart" || restartArgs[1] != "--time" || restartArgs[2] != "30" {
		t.Fatalf("restart without SIGTERM grace window: %v", restartArgs)
	}
	if id, err := hex.DecodeString(restartArgs[3]); err != nil || len(id) != 32 {
		t.Fatalf("restart target is not the immutable container ID: %v", restartArgs)
	}
}

func TestProjectionRestartDefersWhileAnotherLeaseExists(t *testing.T) {
	called := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; t.Error("busy runtime was signalled") }))
	defer api.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	_, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address = api.URL
	rt := entry.Runtime
	m.mu.Unlock()
	applied, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 0)
	if err != nil || applied || called {
		t.Fatal("busy restart", applied, err)
	}
}

func TestProjectionRestartFailureKeepsFreshReadinessRequirement(t *testing.T) {
	for _, failure := range []string{"changed-runtime", "bad-response", "no-marker", "daemon", "readiness"} {
		t.Run(failure, func(t *testing.T) {
			var restarting atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "bad-response" {
					w.WriteHeader(502)
					return
				}
				writeJSON(w, http.StatusOK, map[string]bool{"scheduled": failure != "no-marker"})
			}))
			defer api.Close()
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				if args[0] == "restart" {
					restarting.Store(true)
					if failure == "daemon" {
						return nil, errors.New("daemon refused")
					}
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error {
				if restarting.Load() && failure == "readiness" {
					return errors.New("not ready")
				}
				return nil
			})
			rt, err := m.Ensure(context.Background(), binding(root))
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			entry := m.items[runtimeKey(binding(root))]
			entry.Address, entry.Leases, entry.State = api.URL, 0, Idle
			rt = entry.Runtime
			m.mu.Unlock()
			if failure == "changed-runtime" {
				rt.Generation = "foreign"
			}
			applied, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 0)
			if applied || (err == nil) != (failure == "no-marker") {
				t.Fatal("unsafe restart result", applied, err)
			}
			m.mu.Lock()
			restored := entry.restored
			m.mu.Unlock()
			if (failure == "daemon" || failure == "readiness") && !restored {
				t.Fatal("failure left stale cached readiness")
			}
		})
	}
}

func TestProjectionRestartAcknowledgesBeforeNotifierDeadline(t *testing.T) {
	var restarting atomic.Bool
	ready := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(ready) })
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	}))
	defer native.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "restart" {
			restarting.Store(true)
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error {
		if restarting.Load() {
			<-ready
		}
		return nil
	})
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address, entry.State, entry.Leases, entry.auth = native.URL, Idle, 0, "secret"
	m.mu.Unlock()
	supervisor := httptest.NewServer(m.Handler())
	defer func() { release.Do(func() { close(ready) }); supervisor.Close() }()
	request, _ := http.NewRequest(http.MethodPost, supervisor.URL+"/v1/restart-request", bytes.NewBufferString(`{"principal_id":"alice","context_id":"alice","runtime_id":"alice"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response, err := (&http.Client{Timeout: 750 * time.Millisecond}).Do(request)
	if err != nil {
		t.Fatal("notifier did not receive accepted marker before readiness", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(body) != `{"applied":false,"scheduled":true}` {
		t.Fatal("incorrect acceptance contract", string(body), err)
	}
	release.Do(func() { close(ready) })
}

// A notifier whose deadline already fired cannot collect the ack, so its
// queued retry must not spend the acquisition lock on a daemon restart —
// timed-out posts would otherwise serialize into a convoy that starves
// Ensure/wake. The durable marker is still written and applies later.
func TestRestartRequestSkipsRestartForDeadNotifier(t *testing.T) {
	markerDone := make(chan struct{})
	var markerOnce sync.Once
	applied := false
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/restart" {
			applied = true
		}
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	}))
	defer runtimeAPI.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address, entry.State, entry.Leases, entry.auth = runtimeAPI.URL, Idle, 0, "secret"
	m.mu.Unlock()
	base := m.cfg.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	m.cfg.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if r.URL.Path == "/v1/restart-request" {
			markerOnce.Do(func() { close(markerDone) })
		}
		return response, err
	})}
	lock := m.lockFor(runtimeKey(binding(root)))
	lock.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := json.Marshal(map[string]string{"principal_id": "alice", "context_id": "alice", "runtime_id": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/restart-request", bytes.NewReader(body)).WithContext(ctx)
	httpRequest.Header.Set("Authorization", "Bearer secret")
	done := make(chan struct{})
	go func() {
		m.Handler().ServeHTTP(httptest.NewRecorder(), httpRequest)
		close(done)
	}()
	select {
	case <-markerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("restart-request never marked the runtime")
	}
	// The notifier's deadline fires while its request sits in the lock queue;
	// once it wins the lock it must leave instead of restarting.
	cancel()
	lock.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dead notifier stayed queued behind the restart lock")
	}
	if applied {
		t.Fatal("dead notifier triggered a daemon restart")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagedRestartMaterializesChangedDisplayOverlay(t *testing.T) {
	t.Setenv("HUB_ENV", "prod")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"scheduled":true}`) }))
	defer api.Close()
	var configPath string
	checked := false
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "restart" {
			body, err := os.ReadFile(configPath)
			if err != nil || !strings.Contains(string(body), "long_running_notifications: false") {
				t.Error("managed restart did not materialize the changed display overlay", err)
			}
			checked = true
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	_, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	writeManagedFixtures(t, root)
	configPath, err = m.materializeHermesConfig(binding(root), "prod")
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(root, "managed", "prod", "runtime")
	if err := os.MkdirAll(overlay, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "self-settings.json"), []byte(`{"display.long_running_notifications":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address = api.URL
	entry.Managed = true
	rt := entry.Runtime
	m.mu.Unlock()
	applied, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 1)
	if err != nil || !applied || !checked {
		t.Fatal(applied, checked, err)
	}
}

// Every accepted restart is counted on the persisted runtime record — /status
// renders the count so an operator sees restart churn (SPEC-0053 R3.6).
func TestAcceptedRestartCountedOnRuntime(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	}))
	defer api.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	if _, err := m.Ensure(context.Background(), binding(root)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(binding(root))]
	entry.Address, entry.Leases, entry.State = api.URL, 0, Idle
	rt := entry.Runtime
	m.mu.Unlock()
	applied, err := m.applyRuntimeRestart(context.Background(), rt, "secret", 0)
	if err != nil || !applied {
		t.Fatal(applied, err)
	}
	m.mu.Lock()
	restarts := m.items[runtimeKey(binding(root))].Runtime.Restarts
	m.mu.Unlock()
	if restarts != 1 {
		t.Fatalf("restarts=%d, want 1", restarts)
	}
}
