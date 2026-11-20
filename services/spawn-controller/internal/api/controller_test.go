package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// fakeBackend records spawns without launching processes; every workload
// it "starts" points at the test's runtime endpoint so the forwarded
// call really travels.
type fakeBackend struct {
	mu      sync.Mutex
	started []backend.Spec
	live    map[string]backend.Runtime
	address string
}

func (f *fakeBackend) Start(_ context.Context, spec backend.Spec) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	rt := backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Address: f.address}
	if f.live == nil {
		f.live = map[string]backend.Runtime{}
	}
	f.live[spec.Key] = rt
	return rt, nil
}

func (f *fakeBackend) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, id)
	return nil
}

func (f *fakeBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rt, ok := f.live[id]; ok {
		return rt, nil
	}
	return backend.Runtime{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
}

func (f *fakeBackend) List(context.Context) ([]backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backend.Runtime, 0, len(f.live))
	for _, rt := range f.live {
		out = append(out, rt)
	}
	return out, nil
}

func (f *fakeBackend) Exec(_ context.Context, id string, _ backend.ExecSpec) (backend.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return backend.ExecResult{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	return backend.ExecResult{}, nil
}

func (f *fakeBackend) VerifyWorker(_ context.Context, id string, _ backend.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	return nil
}

var testTenant = store.Tenant{Org: "org-a", Owner: "alice"}

// controllerForTest wires the durable controller over a real store and a
// fake backend whose runtimes answer on runtimeURL; plant spaces with
// plantSpace.
func controllerForTest(t *testing.T, token string, max int, runtimeURL string) (*Controller, *store.Store, *fakeBackend) {
	t.Helper()
	st := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	be := &fakeBackend{address: runtimeURL}
	rts := &spawn.Runtimes{Store: st, Backend: be, Image: "harness-runtime", MaxLive: max}
	return &Controller{runtimes: rts, token: token, client: &http.Client{Timeout: time.Second}}, st, be
}

// plantSpace writes the space row claim reads: for the api surface a
// stored effective digest is enough — the full revision bootstrap is
// covered in the spawn package.
func plantSpace(t *testing.T, s *store.Store, tenant store.Tenant, spaceID, digest string) {
	t.Helper()
	err := s.Tx(context.Background(), func(tx *store.Tx) error {
		return tx.Scoped(tenant).Insert("spaces", []string{"id", "created_at", "effective_digest"}, spaceID, time.Now().Unix(), digest)
	})
	if err != nil {
		t.Fatalf("plant space: %v", err)
	}
}

func leaseCount(t *testing.T, s *store.Store, tenant store.Tenant, runtimeID string) int {
	t.Helper()
	var n int
	if err := s.ReadTx(context.Background(), func(tx *store.Tx) error {
		var err error
		n, err = spawn.LeaseCount(tx.Scoped(tenant), runtimeID)
		return err
	}); err != nil {
		t.Fatalf("lease count: %v", err)
	}
	return n
}

func runtimeState(t *testing.T, s *store.Store, tenant store.Tenant, id string) string {
	t.Helper()
	var state string
	if err := s.ReadTx(context.Background(), func(tx *store.Tx) error {
		return tx.Scoped(tenant).QueryRow("SELECT state FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&state)
	}); err != nil {
		t.Fatalf("runtime state: %v", err)
	}
	return state
}

// The service surface refuses what a stray browser or a leaked port could
// send: browser Origins, missing or wrong tokens, non-JSON bodies. Only a
// bearer-carrying JSON POST reaches the runtime path.
func TestServiceSurfaceRequiresTokenAndRefusesBrowser(t *testing.T) {
	c, _, be := controllerForTest(t, "service-token", 4, "http://127.0.0.1:1")
	srv := httptest.NewServer(c.routes())
	defer srv.Close()
	post := func(headers map[string]string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages?org=org-a&owner=alice", strings.NewReader(`{"space":"s","agent":"a","text":"x"}`))
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
	if n := len(be.started); n != 0 {
		t.Fatalf("refused calls spawned %d runtimes", n)
	}
	// runtimes list needs the token too.
	for _, headers := range []map[string]string{
		{},
		{"Authorization": "Bearer wrong"},
		{"Authorization": "Bearer service-token", "Origin": "null"},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/runtimes?org=org-a&owner=alice", nil)
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

// A message executes claim -> acquire -> forward -> release: the runtime
// endpoint answers through the hold, and afterwards the row is warm with
// no leases left on it.
func TestMessageClaimsHoldsAndReleases(t *testing.T) {
	var forwarded atomic.Int64
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("forwarded call: %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		forwarded.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":"pong"}`))
	}))
	defer child.Close()
	c, st, be := controllerForTest(t, "service-token", 4, child.URL)
	plantSpace(t, st, testTenant, "s1", "sha256:abc")
	srv := httptest.NewServer(c.routes())
	defer srv.Close()

	post := func(url, body string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer service-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	status, body := post(srv.URL+"/v1/messages?org=org-a&owner=alice", `{"space":"s1","agent":"a1","text":"ping"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"reply":"pong"`) {
		t.Fatalf("message: status %d body %s", status, body)
	}
	if forwarded.Load() != 1 {
		t.Fatalf("forwarded %d calls, want 1", forwarded.Load())
	}
	if len(be.started) != 1 {
		t.Fatalf("backend started %d runtimes", len(be.started))
	}
	runtimeID := be.started[0].Key
	if got := runtimeState(t, st, testTenant, runtimeID); got != "warm" {
		t.Fatalf("post-message runtime is %s, want warm", got)
	}
	if n := leaseCount(t, st, testTenant, runtimeID); n != 0 {
		t.Fatalf("release left %d lease rows", n)
	}

	// The runtime inventory is tenant-scoped rows now: the warm row shows
	// under the owner and nothing shows under another tenant.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/runtimes?org=org-a&owner=alice", nil)
	req.Header.Set("Authorization", "Bearer service-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"space_id":"s1"`) || !strings.Contains(string(body), `"state":"warm"`) {
		t.Fatalf("runtimes inventory: %s", body)
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/runtimes?org=org-a&owner=bob", nil)
	req.Header.Set("Authorization", "Bearer service-token")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(body), `"space_id":"s1"`) {
		t.Fatalf("foreign tenant saw alice's runtime: %s", body)
	}
}

// A space the controller has no effective digest for is refused with the
// localized unavailable — and no workload is ever started for it.
func TestMessageWithoutEffectiveSpecRefused(t *testing.T) {
	c, st, be := controllerForTest(t, "service-token", 4, "http://127.0.0.1:1")
	plantSpace(t, st, testTenant, "empty", "")
	srv := httptest.NewServer(c.routes())
	defer srv.Close()
	for _, path := range []string{
		"/v1/messages?org=org-a&owner=alice", // space exists, never activated
		"/v1/messages?org=org-a&owner=alice", // space missing entirely
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(`{"space":"empty","agent":"a","text":"x"}`))
		req.Header.Set("Authorization", "Bearer service-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d body %s", path, resp.StatusCode, body)
		}
	}
	if n := len(be.started); n != 0 {
		t.Fatalf("refused claims started %d runtimes", n)
	}
}

// A held lease renews through the heartbeat endpoint; a lease the row no
// longer carries is not_found, a foreign tenant's renew sees nothing.
func TestLeaseRenewEndpoint(t *testing.T) {
	c, st, _ := controllerForTest(t, "service-token", 4, "http://127.0.0.1:1")
	plantSpace(t, st, testTenant, "s1", "sha256:abc")
	srv := httptest.NewServer(c.routes())
	defer srv.Close()
	ctx := context.Background()

	rt, err := c.runtimes.Claim(ctx, testTenant, "s1", "a1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := c.runtimes.Acquire(ctx, testTenant, rt.ID, "ls-test"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	post := func(path string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer service-token")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	status, body := post("/v1/leases/ls-test/renew?org=org-a&owner=alice")
	if status != http.StatusOK {
		t.Fatalf("renew: status %d body %s", status, body)
	}
	if got := runtimeState(t, st, testTenant, rt.ID); got != "busy" {
		t.Fatalf("renewed runtime is %s", got)
	}
	if status, _ := post("/v1/leases/ls-ghost/renew?org=org-a&owner=alice"); status != http.StatusNotFound {
		t.Fatalf("missing lease renew: %d", status)
	}
	if status, _ := post("/v1/leases/ls-test/renew?org=org-a&owner=bob"); status != http.StatusNotFound {
		t.Fatalf("foreign tenant renew: %d", status)
	}
}

// Claims beyond the live cap are refused; a claim for an already-live
// pair still resolves.
func TestControllerRefusesBeyondRuntimeLimit(t *testing.T) {
	c, st, _ := controllerForTest(t, "service-token", 2, "http://127.0.0.1:1")
	plantSpace(t, st, testTenant, "s1", "sha256:a")
	plantSpace(t, st, testTenant, "s2", "sha256:a")
	plantSpace(t, st, testTenant, "s3", "sha256:a")
	ctx := context.Background()

	if _, err := c.runtimes.Claim(ctx, testTenant, "s1", "a1"); err != nil {
		t.Fatalf("claim s1: %v", err)
	}
	if _, err := c.runtimes.Claim(ctx, testTenant, "s2", "a1"); err != nil {
		t.Fatalf("claim s2: %v", err)
	}
	if _, err := c.runtimes.Claim(ctx, testTenant, "s3", "a1"); !errors.Is(err, httpapi.ErrUnavailable) {
		t.Fatalf("third claim: want unavailable, got %v", err)
	}
	if _, err := c.runtimes.Claim(ctx, testTenant, "s1", "a1"); err != nil {
		t.Fatalf("existing pair refused: %v", err)
	}
}

// The JSON error body stays what the surface always sent.
func TestErrorBodyShape(t *testing.T) {
	c, st, _ := controllerForTest(t, "service-token", 4, "http://127.0.0.1:1")
	plantSpace(t, st, testTenant, "s1", "sha256:abc")
	srv := httptest.NewServer(c.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages?org=org-a", strings.NewReader(`{"space":"s1","agent":"a1","text":"x"}`))
	req.Header.Set("Authorization", "Bearer service-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing owner: %d", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error == "" {
		t.Fatalf("error body: %v %+v", err, body)
	}
}
