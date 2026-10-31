package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelRewritePreservesExactNumbersAndUnknownFields(t *testing.T) {
	original := []byte(`{"model":"logical","seed":9007199254740993,"extension":{"n":12345678901234567890},"messages":[{"role":"user","content":"fixture"}]}`)
	result, err := rewriteModelField(original, "provider-model")
	if err != nil || !bytes.Contains(result, []byte(`9007199254740993`)) || !bytes.Contains(result, []byte(`12345678901234567890`)) || !bytes.Contains(result, []byte(`"model":"provider-model"`)) {
		t.Fatalf("model rewrite changed request data: %s err=%v", result, err)
	}
}

func TestRegistryReloadRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	for _, body := range []string{"providers: {}\nmodels: {}\nsecret_typo: fixture-secret\n", "providers: {}\nmodels: {}\n---\nproviders: {}\nmodels: {}\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		registry := &relayRegistry{path: path}
		_, err := registry.current()
		if err == nil {
			t.Error("invalid registry accepted")
		}
		if err != nil && strings.Contains(err.Error(), "fixture-secret") {
			t.Error("registry error exposed its contents")
		}
	}
}

func TestRegistryOldRequestCannotPoisonReloadedLadder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	write := func(base string) {
		t.Helper()
		body := "providers:\n  p:\n    url: " + base + "\nmodels:\n  default:\n    ladder: [{provider: p, model: provider-model}]\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("https://old.invalid")
	registry := &relayRegistry{path: path}
	old, err := registry.current()
	if err != nil {
		t.Fatal(err)
	}
	write("https://new.invalid")
	registry.mtime = time.Time{}
	current, err := registry.current()
	if err != nil {
		t.Fatal(err)
	}
	registry.ladderFor(old, "default", old.Models["default"])
	hops := registry.ladderFor(current, "default", current.Models["default"])
	if len(hops) != 1 || hops[0].base != "https://new.invalid" {
		t.Fatal("in-flight request cached stale credentials/endpoints after reload")
	}
}

func TestRegistryProviderCooldownAppliesAcrossLogicalModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	body := "providers:\n  account:\n    url: https://provider.invalid\nmodels:\n  first:\n    ladder: [{provider: account, model: model-a}]\n  second:\n    ladder: [{provider: account, model: model-b}]\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	registry := &relayRegistry{path: path}
	file, err := registry.current()
	if err != nil {
		t.Fatal(err)
	}
	first := registry.ladderFor(file, "first", file.Models["first"])
	second := registry.ladderFor(file, "second", file.Models["second"])
	first[0].fail()
	if second[0].available(time.Now()) {
		t.Error("exhausted provider account remains usable through another logical model")
	}
}

func TestModelRelayStreamsFirstEventBeforeProviderFinishes(t *testing.T) {
	for _, registryMode := range []bool{false, true} {
		release := make(chan struct{})
		var finishedEarly atomic.Bool
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(time.Second):
				finishedEarly.Store(true)
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		var handler http.Handler
		var err error
		if registryMode {
			path := filepath.Join(t.TempDir(), "models.yaml")
			body := "providers:\n  p:\n    url: " + upstream.URL + "\n    allow_private: true\nmodels:\n  m:\n    ladder: [{provider: p, model: provider-model}]\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			handler, err = modelRelayRegistryHandler(path, filepath.Join(t.TempDir(), "selection.json"), "alice", "")
		} else {
			hops, e := parseModelHops("p=" + upstream.URL)
			if e != nil {
				t.Fatal(e)
			}
			handler, err = modelRelayHandler(hops, "m")
		}
		if err != nil {
			t.Fatal(err)
		}
		relay := httptest.NewServer(handler)
		response, err := http.Post(relay.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true}`))
		if err != nil {
			close(release)
			relay.Close()
			upstream.Close()
			t.Fatal(err)
		}
		first := make([]byte, len("data: first\n\n"))
		_, err = io.ReadFull(response.Body, first)
		close(release)
		_ = response.Body.Close()
		relay.Close()
		upstream.Close()
		if err != nil || string(first) != "data: first\n\n" || finishedEarly.Load() {
			t.Errorf("stream buffered until completion (registry=%t): %q err=%v", registryMode, first, err)
		}
	}
}

func TestModelHopDeadlineIncludesCredentialResolution(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer provider.Close()
	hops, err := parseModelHops("credential=" + provider.URL + ",fallback=" + provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	hops[0].keyFor = func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	hop, response, err := modelCall(ctx, provider.Client(), hops, http.MethodPost, "/v1/chat/completions", "", "application/json", []byte(`{"model":"fixture"}`), 40*time.Millisecond)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil || hop == nil || hop.name != "fallback" || calls.Load() != 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("credential resolution exhausted the whole ladder: calls=%d elapsed=%s err=%v", calls.Load(), time.Since(started), err)
	}
}

func TestRegistryMissingProviderKeyFailsBeforeUpstreamDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer upstream.Close()
	t.Setenv("M15_REQUIRED_PROVIDER_KEY", "")
	t.Setenv("HUB_MODEL_RELAY_AUTH", "owner-fixture")
	path := filepath.Join(t.TempDir(), "models.yaml")
	body := "providers:\n  p:\n    url: " + upstream.URL + "\n    allow_private: true\n    key_env: M15_REQUIRED_PROVIDER_KEY\nmodels:\n  default:\n    ladder: [{provider: p, model: provider-model}]\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(path, filepath.Join(t.TempDir(), "selection.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer owner-fixture")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 502 || calls.Load() != 0 {
		t.Errorf("missing credential downgraded to anonymous access: status=%d calls=%d", response.Code, calls.Load())
	}
}

func TestModelRelayTimedOutHopFallsBackWithinRequestDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
			_, _ = io.WriteString(w, `{"choices":[]}`)
		}
	}))
	defer slow.Close()
	var fastCalls atomic.Int32
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastCalls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer fast.Close()
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "40ms")
	handler, err := modelRelayHandler(oneHop(t, "slow="+slow.URL+",fast="+fast.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req := chatRequest().WithContext(ctx)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != http.StatusOK || out.Header().Get("X-Model-Hop") != "fast" || fastCalls.Load() != 1 {
		t.Fatalf("stalled hop prevented fallback: code=%d fast=%d", out.Code, fastCalls.Load())
	}
}

func TestModelRelayRegistryRejectsForeignCallerAndUnknownModels(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer upstream.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "alice-fixture")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	contents := fmt.Sprintf("providers:\n  p:\n    url: %s\n    allow_private: true\nmodels:\n  default:\n    ladder: [{provider: p, model: model-one}]\n  private:\n    owners: [bob]\n    ladder: [{provider: p, model: model-two}]\n", upstream.URL)
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "admin-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ auth, model string }{{"bob-fixture", "default"}, {"alice-fixture", "private"}, {"alice-fixture", "unknown"}} {
		t.Run(tc.auth+"/"+tc.model, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+tc.model+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tc.auth)
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != http.StatusForbidden {
				t.Fatalf("ungranted request accepted: %d", out.Code)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("denied calls reached provider: %d", calls.Load())
	}
}

// The default per-hop bound covers the whole exchange including a streamed
// body — agentic completions routinely run tens of seconds, so the default
// must match the request budget, not clip healthy responses mid-stream.
func TestModelRelayDefaultHopBoundCoversLongCompletions(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "")
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "")
	hop, total, err := modelRelayLimits()
	if err != nil {
		t.Fatal(err)
	}
	if hop < time.Minute || hop > total {
		t.Fatalf("default hop bound %s clips long completions (total %s)", hop, total)
	}
}

// Live incident: a book-length HTML generation was clipped at exactly the
// 3-minute default hop bound (cliproxy 499s at 16:38/16:42/16:46) and the
// run failed. One generation must get the maximum permitted hop budget.
func TestModelRelayDefaultHopCoversCappedGeneration(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "")
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "")
	hop, total, err := modelRelayLimits()
	if err != nil {
		t.Fatal(err)
	}
	if hop < 5*time.Minute || total <= hop {
		t.Fatalf("default bounds clip long generations: hop %s total %s", hop, total)
	}
}

func TestModelRelayRejectsUnboundedConfiguration(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_REGISTRY", "")
	t.Setenv("HUB_MODEL_RELAY_MODEL", "synthetic")
	for _, tc := range []struct{ name, key, value string }{
		{"zero_hop", "HUB_MODEL_RELAY_HOP_TIMEOUT", "0s"},
		{"invalid_hop", "HUB_MODEL_RELAY_HOP_TIMEOUT", "unknown"},
		{"excessive_hop", "HUB_MODEL_RELAY_HOP_TIMEOUT", "6m"},
		{"zero_total", "HUB_MODEL_RELAY_TIMEOUT", "0s"},
		{"invalid_total", "HUB_MODEL_RELAY_TIMEOUT", "unknown"},
		{"excessive_total", "HUB_MODEL_RELAY_TIMEOUT", "11m"},
		{"hop_exceeds_total", "HUB_MODEL_RELAY_TIMEOUT", "1ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "")
			t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "")
			t.Setenv(tc.key, tc.value)
			if _, err := modelRelayHandlerFromEnv(); err == nil {
				t.Fatal("unbounded timeout configuration accepted")
			}
		})
	}
	var hops []string
	for i := range 9 {
		hops = append(hops, fmt.Sprintf("hop%d=http://provider.invalid", i))
	}
	t.Setenv("HUB_MODEL_RELAY_UPSTREAMS", strings.Join(hops, ","))
	if _, err := modelRelayHandlerFromEnv(); err == nil {
		t.Fatal("more than eight hops accepted")
	}
}

func TestModelRelayTotalDeadlineStopsRemainingHops(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
			http.Error(w, "quota", http.StatusTooManyRequests)
		}
	}))
	defer slow.Close()
	var calls atomic.Int32
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer fast.Close()
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "400ms")
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "500ms")
	handler, err := modelRelayHandler(oneHop(t, "first="+slow.URL+",second="+slow.URL+",last="+fast.URL), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, chatRequest())
	if out.Code != http.StatusBadGateway || calls.Load() != 0 {
		t.Fatalf("total deadline ignored: status=%d last-hop-calls=%d", out.Code, calls.Load())
	}
}

func TestModelRelayRuntimeAliasUsesGrantedSelection(t *testing.T) {
	var saw string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		saw = string(body)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "runtime-fixture")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	selection := filepath.Join(dir, "selection.json")
	yaml := fmt.Sprintf("runtime_model: pinned-runtime-model\nproviders:\n  p:\n    allow_private: true\n    url: %s\nmodels:\n  default:\n    ladder: [{provider: p, model: provider-one}]\n  selected:\n    owners: [alice]\n    ladder: [{provider: p, model: provider-two}]\n", upstream.URL)
	if err := os.WriteFile(file, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selection, []byte(`{"model":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, selection, "alice", "admin-fixture")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"pinned-runtime-model"}`))
	req.Header.Set("Authorization", "Bearer runtime-fixture")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(saw, `"provider-two"`) {
		t.Fatalf("runtime alias ignored granted selection: code=%d body=%s", out.Code, saw)
	}
}

func TestRegistryRelayUsesProviderCredentialInsteadOfRuntimeBearer(t *testing.T) {
	received := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "runtime-fixture")
	t.Setenv("OPENAI_API_KEY", "provider-fixture")
	t.Setenv("HUB_MODEL_RELAY_CLIPROXY", upstream.URL)
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(file, []byte("models:\n  default:\n    ladder: [{provider: cliproxy, model: provider-model}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "admin-fixture")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
	req.Header.Set("Authorization", "Bearer runtime-fixture")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("provider call rejected: %d", out.Code)
	}
	if got := <-received; got != "Bearer provider-fixture" {
		t.Error("runtime bearer was forwarded instead of the provider credential")
	}
}

func TestRegistryModelConfigurationReloadFailsClosed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	valid := "providers:\n  p: {url: https://provider.invalid}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n"
	if err := os.WriteFile(file, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	registry := &relayRegistry{path: file, ladders: map[string][]*modelHop{}}
	if _, err := registry.current(); err != nil {
		t.Fatal(err)
	}
	invalid := "providers:\n  p: {url: 'https://user:fixture-secret@provider.invalid'}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n"
	if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	changed := registry.mtime.Add(2 * time.Second)
	if err := os.Chtimes(file, changed, changed); err != nil {
		t.Fatal(err)
	}
	_, err := registry.current()
	if err == nil {
		t.Fatal("unsafe provider URL accepted on live registry reload")
	}
	if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("invalid registry error discloses credential material")
	}
}

func TestRegistryRelayPrivateEndpointRequiresExplicitTrust(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_AUTH", "owner-fixture")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer upstream.Close()
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint(trusted), func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "models.yaml")
			content := fmt.Sprintf("providers:\n  p: {url: '%s', allow_private: %t}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n", upstream.URL, trusted)
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
			req.Header.Set("Authorization", "Bearer owner-fixture")
			req.Header.Set("Content-Type", "application/json")
			before := calls.Load()
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if trusted {
				if out.Code != 200 || calls.Load() != before+1 {
					t.Fatalf("explicit local provider unavailable: %d", out.Code)
				}
			} else if out.Code != 502 || calls.Load() != before {
				t.Fatalf("untrusted private endpoint reached: status=%d calls=%d", out.Code, calls.Load()-before)
			}
		})
	}
}

func TestRegistryRelayExplicitPrivateLANEndpoint(t *testing.T) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	host := ""
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && ip.IsPrivate() {
			host = ip.String()
			break
		}
	}
	if host == "" {
		t.Skip("host has no private IPv4 interface for the local-only HTTP fixture")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"choices":[]}`) }))
	if err := upstream.Listener.Close(); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "owner-fixture")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	content := fmt.Sprintf("providers:\n  p: {url: '%s', allow_private: true}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n", upstream.URL)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
	req.Header.Set("Authorization", "Bearer owner-fixture")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("explicitly trusted private LAN service blocked: %d", out.Code)
	}
}

func TestRegistryRelayDoesNotLeakRuntimeBearerToAnonymousProvider(t *testing.T) {
	received := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "runtime-fixture")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	content := fmt.Sprintf("providers:\n  p: {url: '%s', allow_private: true}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n", upstream.URL)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
	req.Header.Set("Authorization", "Bearer runtime-fixture")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("anonymous provider rejected: %d", out.Code)
	}
	if <-received != "" {
		t.Fatal("internal runtime credential crossed the provider boundary")
	}
}

func TestModelRelayProviderBaseURLPreservesAPIPrefix(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	for suffix, expected := range map[string]string{"": "/v1/chat/completions", "/v1": "/v1/chat/completions", "/v1beta/openai": "/v1beta/openai/chat/completions"} {
		t.Run(suffix, func(t *testing.T) {
			handler, err := modelRelayHandler(oneHop(t, "provider="+upstream.URL+suffix), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, chatRequest())
			if out.Code != 200 {
				t.Fatalf("provider call failed: %d", out.Code)
			}
			if got := <-seen; got != expected {
				t.Fatalf("API prefix changed: got %q want %q", got, expected)
			}
		})
	}
}

func TestRegistryRelayHealthAndCatalogAreOwnerAuthenticated(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_AUTH", "owner-fixture")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	content := "models:\n  default:\n    ladder: [{provider: cliproxy, model: reviewed}]\n"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "admin-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/models", "/v1/hops"} {
		t.Run(path, func(t *testing.T) {
			for _, token := range []string{"foreign-fixture", "owner-fixture", "admin-fixture"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, req)
				expected := 200
				if token == "foreign-fixture" {
					expected = 403
				}
				if out.Code != expected {
					t.Errorf("metadata authorization: token-kind=%t got=%d want=%d", token == "foreign-fixture", out.Code, expected)
				}
			}
		})
	}
}

func TestRegistryProviderGrantDoesNotFollowSharedModelProfile(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer upstream.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	content := fmt.Sprintf("providers:\n  p: {url: '%s', allow_private: true, owners: [alice]}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n", upstream.URL)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"alice", "bob"} {
		t.Run(owner, func(t *testing.T) {
			t.Setenv("HUB_MODEL_RELAY_AUTH", owner+"-fixture")
			handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, owner+".json"), owner, "")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
			req.Header.Set("Authorization", "Bearer "+owner+"-fixture")
			req.Header.Set("Content-Type", "application/json")
			before := calls.Load()
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if owner == "alice" {
				if out.Code != 200 || calls.Load() != before+1 {
					t.Errorf("granted owner denied: %d", out.Code)
				}
			} else if out.Code != 403 || calls.Load() != before {
				t.Errorf("shared model expanded provider rights: status=%d provider-calls=%d", out.Code, calls.Load()-before)
			}
		})
	}
}

func TestRegistryModelRelayTotalDeadlineStopsRemainingHops(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
			http.Error(w, "quota", http.StatusTooManyRequests)
		}
	}))
	defer slow.Close()
	var calls atomic.Int32
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"choices":[]}`) }))
	defer fast.Close()
	t.Setenv("HUB_MODEL_RELAY_AUTH", "owner-fixture")
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "400ms")
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "500ms")
	dir := t.TempDir()
	file := filepath.Join(dir, "models.yaml")
	content := fmt.Sprintf("providers:\n  slow: {url: '%s', allow_private: true}\n  second: {url: '%s', allow_private: true}\n  fast: {url: '%s', allow_private: true}\nmodels:\n  default:\n    ladder: [{provider: slow, model: reviewed}, {provider: second, model: reviewed}, {provider: fast, model: reviewed}]\n", slow.URL, slow.URL, fast.URL)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "selection.json"), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
	req.Header.Set("Authorization", "Bearer owner-fixture")
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 502 || calls.Load() != 0 {
		t.Fatalf("registry total deadline ignored: status=%d final-provider-calls=%d", out.Code, calls.Load())
	}
}
