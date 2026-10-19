package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// registryFixture writes a providers:p registry plus the caller's models
// entries and returns the registry path, selection path and stub upstream.
func registryFixture(t *testing.T, entries string) (string, string, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n  p:\n    url: " + upstream.URL + "\nmodels:\n" + entries
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return registry, filepath.Join(dir, "sel.json"), upstream
}

func decodeModel(r *http.Request) string {
	defer r.Body.Close()
	var doc struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&doc)
	return doc.Model
}

// The registry relay keeps the same review-contract fences as the env relay:
// no queries, no absolute-form targets, no upgrades, no other paths.
func TestModelRelayRegistryKeepsStrictContract(t *testing.T) {
	registry, sel, upstream := registryFixture(t, "  default:\n    ladder:\n      - {provider: p, model: m1}\n")
	defer upstream.Close()
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	for i, mutate := range []func(*http.Request){
		func(r *http.Request) { r.URL.RawQuery = "x=1" },
		func(r *http.Request) { r.Header.Set("Upgrade", "websocket") },
		func(r *http.Request) { r.URL.Host = "evil.invalid" },
		func(r *http.Request) { r.URL.Scheme = "http" },
		func(r *http.Request) { r.Method = http.MethodDelete },
		func(r *http.Request) { r.URL.Path = "/v1/admin" },
	} {
		req := chatRequest()
		mutate(req)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code == http.StatusOK {
			t.Fatalf("case %d: registry relay served a non-contract request", i)
		}
	}
}

// The registry client shares the static relay's verdict path; a reachable
// redirect target proves the client never follows (the 169.254.169.254
// variant only goes red where IMDS answers).
func TestModelRelayRegistryNeverFollowsReachableRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL + "/meta-data")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n  p:\n    url: " + upstream.URL + "\nmodels:\n  synthetic:\n    ladder:\n      - {provider: p, model: m1}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" {
		t.Fatalf("registry upstream redirect escaped: %d %v", response.Code, response.Header())
	}
}

// A request naming an explicitly granted logical model routes to that
// ladder, beating the saved selection.
func TestModelRelayRegistryRequestModelWinsOverSelection(t *testing.T) {
	var sawModel string
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawModel = decodeModel(r)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  a:
    url: http://127.0.0.1:1
  b:
    url: ` + second.URL + `
models:
  default:
    ladder:
      - {provider: a, model: m1}
  turbo:
    owners: [alice]
    ladder:
      - {provider: b, model: gpt-5}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"turbo"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || sawModel != "gpt-5" {
		t.Fatalf("granted request model not routed: %d %q", response.Code, sawModel)
	}
}

// A request naming a model granted only to others falls back to the owner's
// selection/default rather than serving it.
func TestModelRelayRegistryUngrantedRequestFallsBack(t *testing.T) {
	var sawModel string
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawModel = decodeModel(r)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer p.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  p:
    url: ` + p.URL + `
models:
  default:
    ladder:
      - {provider: p, model: safe}
  secret:
    owners: [bob]
    ladder:
      - {provider: p, model: secret-m}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || sawModel != "safe" {
		t.Fatalf("ungranted model leak: %d %q", response.Code, sawModel)
	}
}

// The catalog lists only models granted to this owner.
func TestModelRelayRegistryCatalogHidesUngranted(t *testing.T) {
	registry, sel, upstream := registryFixture(t, `  default:
    ladder:
      - {provider: p, model: m1}
  secret:
    owners: [bob]
    ladder:
      - {provider: p, model: m2}
`)
	defer upstream.Close()
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "default") || strings.Contains(body, "secret") {
		t.Fatalf("catalog leaked or hid wrongly: %d %s", response.Code, body)
	}
}

// Corrupt selection state must not wedge the relay — it falls back to the
// default entry.
func TestModelRelayRegistryCorruptSelectionFallsBack(t *testing.T) {
	registry, sel, upstream := registryFixture(t, "  default:\n    ladder:\n      - {provider: p, model: m1}\n")
	defer upstream.Close()
	if err := os.WriteFile(sel, []byte("not-json{"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("corrupt selection wedged the relay: %d", response.Code)
	}
}

// The builtin cliproxy provider resolves through HUB_MODEL_RELAY_CLIPROXY
// (dedicated topology) when the registry gives it no url.
func TestModelRelayRegistryBuiltinCliproxyURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "models:\n  default:\n    ladder:\n      - {provider: cliproxy, model: gemini-3-pro}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_RELAY_CLIPROXY", upstream.URL)
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || response.Header().Get("X-Model-Hop") != "cliproxy" {
		t.Fatalf("builtin cliproxy provider not resolved: %d %q", response.Code, response.Header().Get("X-Model-Hop"))
	}
}

// key_env on a registry provider injects the bearer resolved from the
// process env (space secrets file in production).
func TestModelRelayRegistryKeyEnvHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n  p:\n    url: " + upstream.URL + "\n    key_env: TEST_RELAY_KEY\nmodels:\n  default:\n    ladder:\n      - {provider: p, model: m1}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_RELAY_KEY", "synthetic-key")
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || gotAuth != "Bearer synthetic-key" {
		t.Fatalf("provider key not forwarded: %d %q", response.Code, gotAuth)
	}
}

// A reload after the registry file changes picks up the new ladder.
func TestModelRelayRegistryReloadsOnChange(t *testing.T) {
	registry, sel, first := registryFixture(t, "  default:\n    ladder:\n      - {provider: p, model: m1}\n")
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	content := "providers:\n  q:\n    url: " + second.URL + "\nmodels:\n  default:\n    ladder:\n      - {provider: q, model: m2}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	// Force a newer mtime so the cache does not serve the stale parse.
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(registry, future, future)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || response.Header().Get("X-Model-Hop") != "q" {
		t.Fatalf("registry reload not picked up: %d %q", response.Code, response.Header().Get("X-Model-Hop"))
	}
}

// /v1/hops reports the resolved ladder state for the owner's model.
func TestModelRelayRegistryHopsHealth(t *testing.T) {
	registry, sel, upstream := registryFixture(t, "  default:\n    ladder:\n      - {provider: p, model: m1}\n")
	defer upstream.Close()
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/hops", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"name":"p"`) {
		t.Fatalf("registry hop health missing: %d %s", response.Code, body)
	}
}

// A selection write that cannot persist must fail loudly, not confirm a
// switch that vanishes on the next request.
func TestModelRelayRegistrySelectionWriteFailure(t *testing.T) {
	registry, _, upstream := registryFixture(t, "  turbo:\n    ladder:\n      - {provider: p, model: m1}\n  default:\n    ladder:\n      - {provider: p, model: m0}\n")
	defer upstream.Close()
	// State path inside a *file*, so MkdirAll/rename cannot succeed.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(blocker, "sel.json"), "alice", "tok")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/model", strings.NewReader(`{"model":"turbo"}`))
	req.Header.Set("Authorization", "Bearer tok")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code == http.StatusOK {
		t.Fatal("selection confirmed while persistence failed")
	}
}
