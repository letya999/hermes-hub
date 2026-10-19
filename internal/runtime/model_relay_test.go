package runtime

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func oneHop(t *testing.T, raw string) []*modelHop {
	t.Helper()
	hops, err := parseModelHops(raw)
	if err != nil {
		t.Fatal(err)
	}
	return hops
}

func TestModelRelayOnlyForwardsReviewedChatRoute(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.String() != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("X-HTTP-Method-Override") != "" || r.Header.Get("X-Original-URL") != "" {
			t.Errorf("unsafe upstream request: %s %s %v", r.Method, r.URL, r.Header)
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(oneHop(t, "one="+upstream.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/v0/management/api-keys", "/v8/management/config", "/management.html", "/v1/models", "/v1/chat/completions?target=/v0/management", "/v1/chat%2fcompletions"} {
		req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusNotFound || calls != 0 {
			t.Fatalf("unreviewed route reached upstream: %s status=%d calls=%d", endpoint, response.Code, calls)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodConnect} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v1/chat/completions", nil))
		if response.Code != http.StatusNotFound || calls != 0 {
			t.Fatalf("unreviewed method reached upstream: %s", method)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Authorization", "Bearer synthetic")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	req.Header.Set("X-Original-URL", "/v0/management")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 || response.Body.String() != `{"choices":[]}` {
		t.Fatalf("reviewed chat route failed: status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	for _, body := range []string{`{}`, `{"model":"other"}`, `{"model":"synthetic","model":"synthetic"}`, `{"model":"synthetic","Model":"other"}`, `{"model":null}`, `{"model":"synthetic"} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden || calls != 1 {
			t.Fatalf("unapproved model reached upstream: %q status=%d calls=%d", body, response.Code, calls)
		}
	}
	tooLarge := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	tooLarge.Header.Set("Content-Type", "application/json")
	tooLarge.ContentLength = (32 << 20) + 1
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tooLarge)
	if response.Code != http.StatusRequestEntityTooLarge || calls != 1 {
		t.Fatal("oversized model request reached upstream")
	}
}

func TestModelRelayCatalogShowsOnlySelectedModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.String() != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("unsafe catalog request: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"synthetic"},{"id":"other"}]}`)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(oneHop(t, "one="+upstream.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer synthetic")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Body.String() != `{"data":[{"id":"synthetic"}]}` {
		t.Fatalf("catalog leaked other models: %d %s", response.Code, response.Body.String())
	}
	missingHandler, err := modelRelayHandler(oneHop(t, "one="+upstream.URL), "missing")
	if err != nil {
		t.Fatal(err)
	}
	missing := httptest.NewRecorder()
	missingHandler.ServeHTTP(missing, req)
	if missing.Code != http.StatusOK || missing.Body.String() != `{"data":[]}` {
		t.Fatalf("missing model was exposed: %d %s", missing.Code, missing.Body.String())
	}
	for _, path := range []string{"/v1/models?all=1", "/v1/models/other"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("catalog route escaped: %s %d", path, response.Code)
		}
	}
}

func TestModelRelayRejectsUpstreamRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(oneHop(t, "one="+upstream.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" {
		t.Fatalf("upstream redirect escaped: %d %v", response.Code, response.Header())
	}
}

// The metadata-endpoint variant only goes red where 169.254.169.254 answers
// (cloud VMs); a reachable local target makes "never follow" falsifiable
// everywhere.
func TestModelRelayNeverFollowsReachableRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/meta-data")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(oneHop(t, "one="+upstream.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" {
		t.Fatalf("upstream redirect escaped: %d %v", response.Code, response.Header())
	}
}

func TestModelRelayIgnoresEnvironmentProxy(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	handler, err := modelRelayHandler(oneHop(t, "one=http://model-relay-proxy-test.invalid:8317"), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || proxyCalls != 0 {
		t.Fatalf("relay used environment proxy: status=%d proxy=%d", response.Code, proxyCalls)
	}
}

func chatRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestModelRelayLadderFallsThroughOnQuota(t *testing.T) {
	firstCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	handler, err := modelRelayHandler(oneHop(t, "a="+first.URL+",b="+second.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || response.Header().Get("X-Model-Hop") != "b" {
		t.Fatalf("ladder did not reach second hop: %d hop=%s", response.Code, response.Header().Get("X-Model-Hop"))
	}
	// Second request: the failed first hop stays in cooldown and is skipped.
	second2 := httptest.NewRecorder()
	handler.ServeHTTP(second2, chatRequest())
	if second2.Code != http.StatusOK || firstCalls != 1 {
		t.Fatalf("cooled-down hop was retried: calls=%d status=%d", firstCalls, second2.Code)
	}
}

func TestModelRelayLadderRelaysClientErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad"}`)
	}))
	defer upstream.Close()
	fallbackCalls := 0
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls++
	}))
	defer fallback.Close()
	handler, err := modelRelayHandler(oneHop(t, "a="+upstream.URL+",b="+fallback.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusBadRequest || fallbackCalls != 0 {
		t.Fatalf("client error triggered fallback: status=%d calls=%d", response.Code, fallbackCalls)
	}
}

func TestModelRelayLadderFallsThroughOnOutageAndAuth(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}))
		handler, err := modelRelayHandler(oneHop(t, "a="+first.URL+",b="+second.URL), "synthetic")
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, chatRequest())
		first.Close()
		second.Close()
		if response.Code != http.StatusOK {
			t.Fatalf("status %d did not fall through the ladder: %d", status, response.Code)
		}
	}
}

func TestModelRelayLadderAllDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer dead.Close()
	handler, err := modelRelayHandler(oneHop(t, "a="+dead.URL+",b="+dead.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusBadGateway {
		t.Fatalf("all-down ladder did not fail closed: %d", response.Code)
	}
}

func TestModelRelayHopKeyEnv(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	t.Setenv("PROVIDER_KEY", "envsecret")
	handler, err := modelRelayHandler(oneHop(t, "ext="+upstream.URL+"|PROVIDER_KEY"), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	req := chatRequest()
	req.Header.Set("Authorization", "Bearer callers")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || gotAuth != "Bearer envsecret" {
		t.Fatalf("key env override failed: status=%d auth=%q", response.Code, gotAuth)
	}
}

func TestModelRelayHopsHealthEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(oneHop(t, "a="+upstream.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected exhausted ladder: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/hops", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"a"`) || !strings.Contains(response.Body.String(), "cooldown_until") {
		t.Fatalf("hop health missing state: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/hops?x=1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("hop health accepted a query string: %d", response.Code)
	}
}

func TestParseModelHops(t *testing.T) {
	for _, spec := range []string{
		"a=ftp://x", "a=http://x@host", "a=http://host?x=1", "a=http://host#f",
		"a=http://host/../x", "A=http://x", "a=b", "a=http://x|lower_env",
		"a=http://x,a=http://y",
	} {
		if _, err := parseModelHops(spec); err == nil {
			t.Fatalf("invalid hop spec accepted: %q", spec)
		}
	}
	hops, err := parseModelHops("")
	if err != nil || len(hops) != 1 || hops[0].name != "cliproxy" || hops[0].base != "http://cliproxy:8317" {
		t.Fatalf("default ladder changed: %v %v", hops, err)
	}
	hops, err = parseModelHops("a=http://x/api,b=https://y:8443|EXT_KEY")
	if err != nil || len(hops) != 2 || hops[0].base != "http://x/api" || hops[1].keyEnv != "EXT_KEY" {
		t.Fatalf("hop parse failed: %v %v", hops, err)
	}
}

func TestModelRelayRegistryLadderRemapsModel(t *testing.T) {
	var sawModel string
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawModel, _ = body["model"].(string)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n  local:\n    url: " + first.URL + "\n  openai:\n    url: " + second.URL + "\n    key_env: TEST_RELAY_KEY\nmodels:\n  default:\n    ladder:\n      - {provider: local, model: qwen3}\n      - {provider: openai, model: gpt-5}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("registry ladder did not serve: %d", response.Code)
	}
	if sawModel != "gpt-5" || response.Header().Get("X-Model-Hop") != "openai" {
		t.Fatalf("hop model not remapped: model=%q hop=%q", sawModel, response.Header().Get("X-Model-Hop"))
	}
}

func TestModelRelayLogsEveryHopAttempt(t *testing.T) {
	var logs strings.Builder
	orig := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(orig) })
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	handler, err := modelRelayHandler(oneHop(t, "a="+first.URL+",b="+second.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	out := logs.String()
	if !strings.Contains(out, "hop=a") || !strings.Contains(out, "429") || !strings.Contains(out, "hop=b") {
		t.Fatalf("missing per-hop audit receipt: %q", out)
	}
}

func TestModelRelayRegistryDeniesUngrantedModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  p:
    url: ` + upstream.URL + `
models:
  default:
    owners: [bob]
    ladder:
      - {provider: p, model: m1}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusForbidden {
		t.Fatalf("ungranted model served: %d", response.Code)
	}
}

func TestModelRelaySelectionEndpointSwitchesModel(t *testing.T) {
	var sawModel string
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawModel, _ = body["model"].(string)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawModel, _ = body["model"].(string)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer second.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  a:
    url: ` + first.URL + `
  b:
    url: ` + second.URL + `
models:
  default:
    ladder:
      - {provider: a, model: qwen3}
  turbo:
    ladder:
      - {provider: b, model: gpt-5}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	sel := filepath.Join(dir, "sel.json")
	handler, err := modelRelayRegistryHandler(registry, sel, "alice", "admintoken")
	if err != nil {
		t.Fatal(err)
	}

	deny := httptest.NewRecorder()
	put := httptest.NewRequest(http.MethodPut, "/v1/model", strings.NewReader(`{"model":"turbo"}`))
	handler.ServeHTTP(deny, put)
	if deny.Code != http.StatusForbidden {
		t.Fatalf("model switch without admin token: %d", deny.Code)
	}

	ok := httptest.NewRecorder()
	put = httptest.NewRequest(http.MethodPut, "/v1/model", strings.NewReader(`{"model":"turbo"}`))
	put.Header.Set("Authorization", "Bearer admintoken")
	handler.ServeHTTP(ok, put)
	if ok.Code != http.StatusOK {
		t.Fatalf("model switch rejected: %d", ok.Code)
	}
	if b, err := os.ReadFile(sel); err != nil || !strings.Contains(string(b), "turbo") {
		t.Fatalf("selection not persisted: %v %q", err, b)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || sawModel != "gpt-5" || response.Header().Get("X-Model-Hop") != "b" {
		t.Fatalf("selection not applied: code=%d model=%q hop=%q", response.Code, sawModel, response.Header().Get("X-Model-Hop"))
	}
}

func TestModelRelayRegistryModeFromEnv(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  p:
    url: ` + upstream.URL + `
models:
  default:
    ladder:
      - {provider: p, model: m1}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_RELAY_REGISTRY", registry)
	t.Setenv("HUB_MODEL_RELAY_STATE", filepath.Join(dir, "sel.json"))
	t.Setenv("HUB_MODEL_RELAY_OWNER", "alice")
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "admintoken")
	handler, err := modelRelayHandlerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("registry mode from env did not serve: %d", response.Code)
	}
}
