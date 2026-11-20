package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
)

// fakeBackend records spawns without launching processes; children it
// "starts" stay listed so the controller's limit and auth paths run for real.
type fakeBackend struct {
	mu      sync.Mutex
	started []backend.SpawnSpec
	stopped []string
}

func (f *fakeBackend) Start(_ context.Context, spec backend.SpawnSpec) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	return backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Address: "http://127.0.0.1:1"}, nil
}

func (f *fakeBackend) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

func (f *fakeBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, spec := range f.started {
		if spec.Key == id {
			return backend.Runtime{ID: id, State: "running"}, nil
		}
	}
	return backend.Runtime{}, nil
}

func (f *fakeBackend) List(context.Context) []backend.Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]backend.Runtime, 0, len(f.started))
	for _, spec := range f.started {
		list = append(list, backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running"})
	}
	return list
}

func controllerForTest(token string, max int) *Controller {
	return &Controller{backend: &fakeBackend{}, token: token, max: max, ensured: map[string]backend.Runtime{}, client: &http.Client{Timeout: time.Second}}
}

// The service surface refuses what a stray browser or a leaked port could
// send: browser Origins, missing or wrong tokens, non-JSON bodies. Only a
// bearer-carrying JSON POST reaches the runtime path.
func TestServiceSurfaceRequiresTokenAndRefusesBrowser(t *testing.T) {
	c := controllerForTest("service-token", 4)
	srv := httptest.NewServer(c.routes())
	defer srv.Close()
	post := func(headers map[string]string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"space":"s","agent":"a","text":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no token", map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
		{"wrong token", map[string]string{"Authorization": "Bearer wrong", "Content-Type": "application/json"}, http.StatusUnauthorized},
		{"browser origin", map[string]string{"Authorization": "Bearer service-token", "Content-Type": "application/json", "Origin": "http://evil.example"}, http.StatusForbidden},
		{"plain form post", map[string]string{"Authorization": "Bearer service-token", "Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := post(tc.headers)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	// The refused calls never reached the backend.
	if n := len(c.backend.(*fakeBackend).started); n != 0 {
		t.Fatalf("refused calls spawned %d runtimes", n)
	}
	// runtimes list needs the token too.
	for _, headers := range []map[string]string{
		{},
		{"Authorization": "Bearer wrong"},
		{"Authorization": "Bearer service-token", "Origin": "null"},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/runtimes", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("runtimes list answered without proper auth: %v", headers)
		}
	}
	// healthz stays unauthenticated for the launcher probe.
	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", resp.StatusCode, err)
	}
	_ = resp.Body.Close()
}

// A message must not mint runtimes without bound: over max, ensure refuses.
func TestControllerRefusesBeyondRuntimeLimit(t *testing.T) {
	c := controllerForTest("service-token", 2)
	for i, pair := range [][2]string{{"s1", "a1"}, {"s2", "a1"}, {"s3", "a1"}} {
		_, err := c.ensure(t.Context(), pair[0], pair[1])
		if i < 2 && err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		if i == 2 && err == nil {
			t.Fatal("third runtime spawned past the limit")
		}
	}
	// The same (space, agent) still resolves — the limit bounds new spawns.
	if _, err := c.ensure(t.Context(), "s1", "a1"); err != nil {
		t.Fatalf("existing runtime refused: %v", err)
	}
}


