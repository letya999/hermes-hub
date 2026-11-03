package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// respawnHTTP is the operator's recreate path for supervised runtimes: an
// idle container is destroyed and re-spawned so env-carried passport config —
// ports, feature flags — takes effect (SPEC-0092 R4.2/A4).
func TestRespawnReplacesIdleRuntime(t *testing.T) {
	var mu sync.Mutex
	runs, removes := 0, 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		switch args[0] {
		case "inspect":
			return nil, os.ErrNotExist
		case "run":
			runs++
		case "rm":
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), normalized); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(normalized)]
	entry.State, entry.Leases = Idle, 0
	m.mu.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice"}`))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"applied":true`) {
		t.Fatalf("respawn status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || removes == 0 {
		t.Fatalf("respawn must remove and re-spawn the container: runs=%d removes=%d", runs, removes)
	}
	m.mu.Lock()
	respawned := m.items[runtimeKey(normalized)]
	leases, state := respawned.Leases, respawned.State
	m.mu.Unlock()
	// Like wake: the respawn is a warm-up, not a hold — the Ensure lease must
	// settle back to idle instead of pinning the fresh runtime busy.
	if leases != 0 || state == Busy {
		t.Fatalf("respawn left a phantom lease: state=%s leases=%d", state, leases)
	}
}

// A busy runtime keeps serving its leases: the respawn must not destroy the
// container mid-lease — it converges on the next natural spawn instead.
func TestRespawnBusyRuntimeDefers(t *testing.T) {
	var mu sync.Mutex
	removes := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "rm" {
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), normalized); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)].State = Busy
	m.mu.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice"}`))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"scheduled":false`) {
		t.Fatalf("busy respawn status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if removes != 0 {
		t.Fatalf("busy runtime was destroyed mid-lease: removes=%d", removes)
	}
}

// The endpoint names a managed runtime by identity: a malformed body or the
// wrong method is a request error, not a silent no-op.
func TestRespawnRejectsMalformedRequests(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	for _, tc := range []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"non-POST", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"garbage body", http.MethodPost, "{not json", http.StatusBadRequest},
		{"bad principal id", http.MethodPost, `{"principal_id":"ALICE!!","context_id":"alice"}`, http.StatusBadRequest},
		{"missing context", http.MethodPost, `{"principal_id":"alice"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "/v1/respawn", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer secret")
			recorder := httptest.NewRecorder()
			m.Handler().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

// A request naming a different runtime_id must not touch the current
// runtime's container — identity is a lookup key, not a wildcard.
func TestRespawnScopedToRuntimeID(t *testing.T) {
	var mu sync.Mutex
	removes := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "rm" {
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), normalized); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(normalized)]
	entry.State, entry.Leases = Idle, 0
	m.mu.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice","runtime_id":"someone-else"}`))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, request)
	mu.Lock()
	defer mu.Unlock()
	if removes != 0 {
		t.Fatalf("respawn removed the container for a foreign runtime_id: removes=%d", removes)
	}
}

// Between the identity lookup and the container removal a lease could flip
// the runtime busy: the respawn must serialize on the runtime key like
// applyRuntimeRestart does, not race a mid-lease destroy.
func TestRespawnSerializesOnRuntimeKey(t *testing.T) {
	var mu sync.Mutex
	removes := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "rm" {
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), normalized); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.items[runtimeKey(normalized)]
	entry.State, entry.Leases = Idle, 0
	m.mu.Unlock()
	lock := m.lockFor(runtimeKey(normalized))
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice"}`))
		request.Header.Set("Authorization", "Bearer secret")
		m.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-done:
		t.Fatal("respawn destroyed the container while the runtime key was held")
	case <-time.After(300 * time.Millisecond):
	}
	mu.Lock()
	removedWhileHeld := removes
	mu.Unlock()
	if removedWhileHeld != 0 {
		t.Fatalf("respawn removed %d containers under a held runtime key", removedWhileHeld)
	}
	lock.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("respawn never completed after the runtime key released")
	}
}

// Respawn destroys and re-creates a container — the right gate must apply as
// it does for restart-request, not silently pass on a decision error.
func TestRespawnRespectsRightsGate(t *testing.T) {
	decide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer decide.Close()
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", decide.URL+"/%s")
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), normalized); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)].State = Idle
	m.mu.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice"}`))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("denied respawn status=%d want 403", recorder.Code)
	}
}
