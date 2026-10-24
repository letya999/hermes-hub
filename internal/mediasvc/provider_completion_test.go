package mediasvc

import (
	"encoding/json"
	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMediaProvidersKeepIndependentEndpointsAndKeys(t *testing.T) {
	remoteCalls, falCalls := 0, 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		if r.Header.Get("Authorization") != "Bearer remote-fixture" {
			t.Errorf("remote received foreign credential")
		}
		http.Error(w, "quota", http.StatusTooManyRequests)
	}))
	defer remote.Close()
	fal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		falCalls++
		if r.Header.Get("Authorization") != "Key fal-fixture" || r.URL.Path != "/fal-ai/portrait" {
			t.Errorf("wrong Fal binding: path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"images":[{"url":"https://blocked.invalid/result.png"}]}`)
	}))
	defer fal.Close()
	providers, _ := json.Marshal(map[string]any{
		"remote": map[string]string{"upstream": remote.URL, "key_env": "REMOTE_FIXTURE_KEY"},
		"fal":    map[string]string{"upstream": fal.URL, "key_env": "FAL_FIXTURE_KEY"},
	})
	t.Setenv("REMOTE_FIXTURE_KEY", "remote-fixture")
	t.Setenv("FAL_FIXTURE_KEY", "fal-fixture")
	t.Setenv("OPENAI_API_KEY", "remote-fixture")
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_ENGINES", "remote,fal")
	t.Setenv("HUB_MEDIA_UPSTREAM", remote.URL)
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	t.Setenv("HUB_MEDIA_MODEL_ROUTES", "portrait=remote:image-model,fal:fal-ai/portrait")
	t.Setenv("HUB_MEDIA_PROVIDERS", string(providers))
	t.Setenv("HUB_MEDIA_FETCH_HOSTS", "allowed.invalid")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	c.BrokerGrant = "unrelated-inherited-grant"
	engine, err := genEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Generate(t.Context(), GenRequest{Model: "portrait", Prompt: "fixture"})
	if remoteCalls != 1 || falCalls != 1 || err == nil || !strings.Contains(err.Error(), "fetch allowlist") {
		t.Fatalf("provider isolation lost: remote=%d fal=%d err=%v", remoteCalls, falCalls, err)
	}
}

func TestExplicitMediaProviderNeverInheritsCredentials(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("anonymous provider inherited a foreign credential")
		}
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"iVBORw0KGgo="}]}`)
	}))
	defer upstream.Close()
	t.Setenv("OPENAI_API_KEY", "foreign-fixture")
	engine, err := genEngine(Config{Engine: "remote", Upstream: upstream.URL, UpstreamKey: "other-fixture", BrokerGrant: "foreign-grant", Providers: map[string]MediaProvider{"remote": {Upstream: upstream.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = engine.Generate(t.Context(), GenRequest{Model: "portrait", Prompt: "fixture"})
	if calls.Load() != 1 {
		t.Fatalf("explicit anonymous provider not called: %d", calls.Load())
	}
}

func TestMediaProviderReferencesRejectMissingOrAmbiguousCredentials(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_UPSTREAM", "https://provider.invalid")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	for _, raw := range []string{`{"remote":{"key_env":"M15_MISSING_KEY"}}`, `{"remote":{"key_env":"OPENAI_API_KEY","broker_grant":"grant-fixture"}}`, `{"remote":{"broker_grant":"../invalid"}}`, `{"remote":{"upstream":"file://provider.invalid"}}`, `{"remote":{"upstream":"https://provider.invalid#fragment"}}`, `{} {}`} {
		t.Setenv("M15_MISSING_KEY", "")
		t.Setenv("HUB_MEDIA_PROVIDERS", raw)
		c, err := ConfigFromEnv(RoleMedia)
		if err == nil {
			_, err = genEngine(c)
		}
		if err == nil {
			t.Errorf("invalid credential or endpoint reference accepted: %s", raw)
		}
	}
}

func TestMediaProviderGrantValidationPrecedesBrokerSetup(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_UPSTREAM", "https://provider.invalid")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	for _, raw := range []string{`{"remote":{"key_env":"OPENAI_API_KEY","broker_grant":"grant-fixture"}}`, `{"remote":{"broker_grant":"../invalid"}}`} {
		t.Setenv("HUB_MEDIA_PROVIDERS", raw)
		if _, err := ConfigFromEnv(RoleMedia); err == nil {
			t.Error("ambiguous or unsafe grant accepted at configuration boundary")
		}
	}
}

func TestMediaProviderQueueDoesNotInheritAnotherEngineEndpoint(t *testing.T) {
	var own, foreign atomic.Int32
	unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreign.Add(1)
		http.Error(w, "foreign queue", http.StatusBadRequest)
	}))
	defer unrelated.Close()
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		own.Add(1)
		if r.URL.Path != "/fal-ai/video" || r.Header.Get("Authorization") != "Key queue-fixture" {
			t.Error("wrong provider queue binding")
		}
		_, _ = io.WriteString(w, `{"request_id":"request-1"}`)
	}))
	defer queue.Close()
	raw, _ := json.Marshal(map[string]any{"fal": map[string]string{"upstream": "https://fal.run", "queue_upstream": queue.URL, "key_env": "M15_QUEUE_KEY"}})
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "fal")
	t.Setenv("HUB_MEDIA_UPSTREAM_KEY", "legacy-fixture")
	t.Setenv("HUB_MEDIA_QUEUE_UPSTREAM", unrelated.URL)
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	t.Setenv("HUB_MEDIA_PROVIDERS", string(raw))
	t.Setenv("M15_QUEUE_KEY", "queue-fixture")
	config, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := genEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := engine.(AsyncEngine).Submit(t.Context(), GenRequest{Kind: "video", Model: "fal-ai/video", Prompt: "fixture"})
	if err != nil || handle.RemoteID != "request-1" || own.Load() != 1 || foreign.Load() != 0 {
		t.Fatalf("queue crossed provider boundary: own=%d foreign=%d err=%v", own.Load(), foreign.Load(), err)
	}
}

func TestExplicitPrimaryProviderNeedsNoLegacyEndpointOrCredential(t *testing.T) {
	for _, name := range []string{"remote", "fal"} {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			scheme := "Bearer "
			if name == "fal" {
				scheme = "Key "
			}
			if r.Header.Get("Authorization") != scheme+"provider-fixture" {
				t.Error("wrong scoped credential")
			}
			http.Error(w, "quota", http.StatusTooManyRequests)
		}))
		raw, _ := json.Marshal(map[string]any{name: map[string]string{"upstream": upstream.URL, "key_env": "M15_PRIMARY_KEY"}})
		for _, key := range []string{"HUB_MEDIA_ENGINES", "HUB_MEDIA_UPSTREAM", "HUB_MEDIA_UPSTREAM_KEY", "HUB_MEDIA_BROKER_GRANT", "FAL_KEY", "OPENAI_API_KEY"} {
			t.Setenv(key, "")
		}
		t.Setenv("HUB_MEDIA_AUTH", "fixture")
		t.Setenv("HUB_MEDIA_ENGINE", name)
		t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
		t.Setenv("HUB_MEDIA_PROVIDERS", string(raw))
		t.Setenv("M15_PRIMARY_KEY", "provider-fixture")
		config, err := ConfigFromEnv(RoleMedia)
		if err != nil {
			upstream.Close()
			t.Errorf("%s explicit provider rejected: %v", name, err)
			continue
		}
		engine, err := genEngine(config)
		if err != nil {
			upstream.Close()
			t.Fatal(err)
		}
		_, err = engine.Generate(t.Context(), GenRequest{Model: "portrait", Prompt: "fixture"})
		upstream.Close()
		if err == nil || calls.Load() != 1 {
			t.Errorf("explicit provider was not reached: calls=%d err=%v", calls.Load(), err)
		}
	}
}

func TestPrimaryFalBrokerReferenceDoesNotRequireLegacyKey(t *testing.T) {
	for _, key := range []string{"HUB_MEDIA_ENGINES", "HUB_MEDIA_UPSTREAM_KEY", "HUB_MEDIA_BROKER_GRANT", "FAL_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "fal")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	t.Setenv("HUB_MEDIA_PROVIDERS", `{"fal":{"upstream":"https://fal.run","broker_grant":"grant-fal"}}`)
	if _, err := ConfigFromEnv(RoleMedia); err != nil {
		t.Fatalf("explicit provider Broker reference rejected before broker resolution: %v", err)
	}
}

func TestSecondaryFalNeverInheritsPrimaryEndpointKeyOrGrant(t *testing.T) {
	t.Setenv("FAL_KEY", "fal-fixture")
	engine, err := genEngine(Config{Engine: "remote", Engines: []string{"remote", "fal"}, Upstream: "https://private-primary.invalid", UpstreamKey: "primary-fixture", Providers: map[string]MediaProvider{"remote": {Upstream: "https://private-primary.invalid"}}})
	if err != nil {
		t.Fatal(err)
	}
	fal := engine.(*genRouter).engines["fal"].(*falGen)
	key, err := fal.apiKey(t.Context())
	if err != nil || key != "fal-fixture" || fal.base != "https://fal.run" || fal.queue != "https://queue.fal.run" {
		t.Errorf("secondary provider inherited primary account settings: base=%s queue=%s err=%v", fal.base, fal.queue, err)
	}
	config := Config{Engine: "command", Engines: []string{"command", "fal"}, Command: "missing-fixture-command", BrokerGrant: "unrelated-primary-grant", Providers: map[string]MediaProvider{"command": {}}}
	engine, err = genEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	fal = engine.(*genRouter).engines["fal"].(*falGen)
	key, err = fal.apiKey(t.Context())
	if err != nil || key != "fal-fixture" {
		t.Error("secondary Fal inherited another engine's Broker grant")
	}
}

func TestSecondaryCustomMediaEngineNeedsItsOwnEndpoint(t *testing.T) {
	t.Setenv("M15_SECONDARY_KEY", "fixture")
	for _, secondary := range []string{"remote", "comfy"} {
		for _, declared := range []bool{false, true} {
			providers := map[string]MediaProvider{"command": {}}
			if declared {
				providers[secondary] = MediaProvider{KeyEnv: "M15_SECONDARY_KEY"}
			}
			_, err := genEngine(Config{Engine: "command", Engines: []string{"command", secondary}, Command: "missing-fixture-command", Upstream: "https://private-primary.invalid", ModelDir: t.TempDir(), Providers: providers})
			if err == nil {
				t.Errorf("%s inherited a primary endpoint (declaration=%t)", secondary, declared)
			}
		}
	}
}

func TestUnsupportedMediaEditsNeverBecomeTextOnlyGeneration(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer upstream.Close()
	for _, name := range []string{"comfy", "fal", "command"} {
		engine, err := genEngine(Config{Engine: name, Upstream: upstream.URL, ModelDir: t.TempDir(), Command: "missing-fixture-command"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = engine.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "edit", Source: testPNG})
		if err == nil || !strings.Contains(err.Error(), "edit") || calls.Load() != 0 {
			t.Errorf("%s silently downgraded edit or failed for wrong reason: calls=%d err=%v", name, calls.Load(), err)
		}
	}
}

func TestMediaProviderConfigurationFailsClosed(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_UPSTREAM", "https://provider.invalid")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	for name, raw := range map[string]string{
		"malformed":      `{`,
		"unknown_engine": `{"ghost":{"upstream":"https://provider.invalid"}}`,
		"inline_secret":  `{"remote":{"upstream":"https://user:secret@provider.invalid"}}`,
		"query_secret":   `{"remote":{"upstream":"https://provider.invalid?secret=x"}}`,
		"queue_secret":   `{"remote":{"queue_upstream":"https://user:secret@provider.invalid"}}`,
		"invalid_env":    `{"remote":{"key_env":"lowercase"}}`,
		"unknown_field":  `{"remote":{"secret":"forbidden"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HUB_MEDIA_PROVIDERS", raw)
			if _, err := ConfigFromEnv(RoleMedia); err == nil {
				t.Fatal("invalid provider configuration accepted")
			}
		})
	}
	t.Setenv("HUB_MEDIA_PROVIDERS", "")
	t.Setenv("HUB_MEDIA_ENGINES", "remote,remote")
	if _, err := ConfigFromEnv(RoleMedia); err == nil {
		t.Fatal("duplicate engines accepted")
	}
}

func TestMediaProvidersUseIndependentBrokerGrants(t *testing.T) {
	var acquired, released, remoteCalls, falCalls atomic.Int32
	broker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/leases" {
			var request brokerv1.AcquireLease
			if json.NewDecoder(r.Body).Decode(&request) != nil || (request.GrantID != "grant-remote" && request.GrantID != "grant-fal") {
				http.Error(w, "wrong grant", http.StatusForbidden)
				return
			}
			acquired.Add(1)
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: request.GrantID, GrantID: request.GrantID})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/release") {
			released.Add(1)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		key := "remote-fixture"
		if strings.Contains(r.URL.Path, "grant-fal") {
			key = "fal-fixture"
		}
		_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"HUB_MEDIA_UPSTREAM_KEY": key}})
	}))
	defer broker.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer remote-fixture" {
			t.Error("remote received foreign key")
		}
		http.Error(w, "quota", http.StatusTooManyRequests)
	}))
	defer remote.Close()
	fal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		falCalls.Add(1)
		if r.Header.Get("Authorization") != "Key fal-fixture" {
			t.Error("Fal received foreign key")
		}
		_, _ = io.WriteString(w, `{"images":[{"url":"https://blocked.invalid/result.png"}]}`)
	}))
	defer fal.Close()
	providers, _ := json.Marshal(map[string]any{
		"remote": map[string]string{"upstream": remote.URL, "broker_grant": "grant-remote"},
		"fal":    map[string]string{"upstream": fal.URL, "broker_grant": "grant-fal"},
	})
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_ENGINES", "remote,fal")
	t.Setenv("HUB_MEDIA_UPSTREAM", remote.URL)
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	t.Setenv("HUB_MEDIA_MODEL_ROUTES", "portrait=remote:image-model,fal:fal-ai/portrait")
	t.Setenv("HUB_MEDIA_PROVIDERS", string(providers))
	t.Setenv("HUB_MEDIA_FETCH_HOSTS", "allowed.invalid")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	c.Broker = mediaBrokerConfig(t, broker)
	c.PrincipalID = "alice"
	c.ContextID = "alice"
	c.RuntimeID = "alice"
	c.PolicyVersion = "hub-media"
	c.BrokerGrant = "grant-unrelated"
	engine, err := genEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Generate(t.Context(), GenRequest{Model: "portrait", Prompt: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "fetch allowlist") || remoteCalls.Load() != 1 || falCalls.Load() != 1 || acquired.Load() != 2 || released.Load() != 2 {
		t.Fatalf("media broker scopes lost: remote=%d fal=%d acquired=%d released=%d err=%v", remoteCalls.Load(), falCalls.Load(), acquired.Load(), released.Load(), err)
	}
}
