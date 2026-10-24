package stack

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedModelSettingsNeverShareOwnerStateOrRotation(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"alice", "bob"} {
		dir := filepath.Join(spaces, owner)
		if err := InitEnvironment(dir, owner, "prod"); err != nil {
			t.Fatal(err)
		}
		settings, err := ReadEnvironment(dir, "prod")
		if err != nil {
			t.Fatal(err)
		}
		settings.CapabilityMode = "managed"
		settings.CapabilityProfileID = "shared-model-profile"
		settings.CapabilityGeneration = 1
		settings.Model = "shared-model"
		settings.ModelURL = "http://model-relay:8318/v1"
		if err := WriteSpace(dir, settings); err != nil {
			t.Fatal(err)
		}
		selection := fmt.Sprintf(`{"schema":1,"user":%q,"environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`, owner)
		if err := os.WriteFile(filepath.Join(dir, "execution.prod.json"), []byte(selection), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeEnvFile(filepath.Join(dir, "secrets.prod.env"), map[string]string{"OPENAI_API_KEY": owner + "-fixture"}); err != nil {
			t.Fatal(err)
		}
		if err := RenderEnvironment(dir, root, "prod"); err != nil {
			t.Fatal(err)
		}
	}
	alice, bob := filepath.Join(spaces, "alice"), filepath.Join(spaces, "bob")
	aliceAuth, err := ReadSecrets(filepath.Join(alice, "model-relay.auth"))
	if err != nil {
		t.Fatal(err)
	}
	bobAuth, err := ReadSecrets(filepath.Join(bob, "model-relay.auth"))
	if err != nil {
		t.Fatal(err)
	}
	if aliceAuth["HUB_MODEL_RELAY_AUTH"] == bobAuth["HUB_MODEL_RELAY_AUTH"] || aliceAuth["HUB_MODEL_RELAY_ADMIN"] == bobAuth["HUB_MODEL_RELAY_ADMIN"] {
		t.Error("shared model profile shared owner authority")
	}
	snapshot := map[string]string{}
	for _, name := range []string{"runtime.prod.env", "managed-runtime.prod.env", "model-relay.auth", "runtime.auth", "generated/model-registry/prod/models.yaml"} {
		body, err := os.ReadFile(filepath.Join(bob, name))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[name] = string(body)
	}
	if err := writeEnvFile(filepath.Join(alice, "secrets.prod.env"), map[string]string{"OPENAI_API_KEY": "alice-rotated-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(alice, root, "prod"); err != nil {
		t.Fatal(err)
	}
	for name, before := range snapshot {
		after, err := os.ReadFile(filepath.Join(bob, name))
		if err != nil || string(after) != before {
			t.Errorf("Alice rotation changed Bob %s: %v", name, err)
		}
	}
	provider, err := ReadSecrets(filepath.Join(alice, "runtime.prod.env"))
	if err != nil || provider["OPENAI_API_KEY"] != "alice-rotated-fixture" {
		t.Error("owner provider rotation not rendered")
	}
	stable, err := ReadSecrets(filepath.Join(alice, "model-relay.auth"))
	if err != nil || stable["HUB_MODEL_RELAY_AUTH"] != aliceAuth["HUB_MODEL_RELAY_AUTH"] {
		t.Error("provider rotation changed runtime bearer")
	}
}

// spaces/models.yaml + spaces/<org>/models.yaml + spaces/<user>/models.yaml
// merge into one registry: later layers win on the same keys.
func TestModelRegistryLayersMerge(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	if err := os.MkdirAll(spaces, 0700); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(spaces, "models.yaml"), map[string]any{
		"providers": map[string]any{
			"openai": map[string]any{"url": "https://api.openai.com/v1", "key_env": "OPENAI_KEY"},
			"local":  map[string]any{"url": "http://local-llm:8080"},
		},
		"models": map[string]any{
			"default": map[string]any{"ladder": []any{map[string]any{"provider": "cliproxy", "model": "gemini-3-pro"}, map[string]any{"provider": "openai", "model": "gpt-5"}}},
			"cheap":   map[string]any{"ladder": []any{map[string]any{"provider": "local", "model": "qwen3"}}},
		},
	})
	org := filepath.Join(spaces, "acme")
	if err := InitEnvironment(org, "acme", "prod"); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(org, "models.yaml"), map[string]any{
		"models": map[string]any{
			"cheap": map[string]any{"owners": []string{"alice"}, "ladder": []any{map[string]any{"provider": "aistudio", "model": "gemini-2.5-flash"}}},
		},
		"providers": map[string]any{
			"aistudio": map[string]any{"url": "https://generativelanguage.googleapis.com/v1beta/openai", "key_env": "GOOGLE_AI_KEY"},
		},
	})
	user := filepath.Join(spaces, "alice")
	if err := InitEnvironment(user, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	// Org dir is not a user space — alice declares her org by settings.
	s, err := ReadEnvironment(user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Organization = "acme"
	if err := WriteSpace(user, s); err != nil {
		t.Fatal(err)
	}

	reg, err := LoadModelRegistry(user, s)
	if err != nil {
		t.Fatal(err)
	}
	// Shared ladder kept.
	if got := reg.Models["default"].Ladder; len(got) != 2 || got[0].Provider != "cliproxy" || got[1].Model != "gpt-5" {
		t.Fatalf("default ladder: %+v", got)
	}
	// Org layer replaced the cheap ladder and granted it to alice only.
	cheap := reg.Models["cheap"]
	if len(cheap.Ladder) != 1 || cheap.Ladder[0].Provider != "aistudio" || !cheap.Allows("alice") || cheap.Allows("bob") {
		t.Fatalf("org override/grant lost: %+v", cheap)
	}
	if reg.Providers["aistudio"].KeyEnv != "GOOGLE_AI_KEY" {
		t.Fatalf("org provider missing: %+v", reg.Providers)
	}
}

// The switch grant rides the same layered owner scheme as models and
// providers: an owner may get none (automatic ladders only), logical
// (granted registry names) or catalog (any model the account serves).
// Layers merge per owner key; unknown levels and owner ids are rejected.
func TestModelRegistrySwitchLevels(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	if err := os.MkdirAll(spaces, 0700); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(spaces, "models.yaml"), map[string]any{
		"switch": map[string]any{"alice": "catalog", "bob": "none", "*": "logical"},
		"models": map[string]any{
			"default": map[string]any{"ladder": []any{map[string]any{"provider": "cliproxy", "model": "gemini-3-flash"}}},
		},
	})
	user := filepath.Join(spaces, "alice")
	if err := InitEnvironment(user, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	// The owner layer narrows her own grant; other keys merge through.
	saveYAML(t, filepath.Join(user, "models.yaml"), map[string]any{
		"switch": map[string]any{"alice": "logical"},
	})
	s, err := ReadEnvironment(user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistry(user, s)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Switch["alice"] != "logical" || reg.Switch["bob"] != "none" || reg.Switch["*"] != "logical" {
		t.Fatalf("switch grants not merged per owner: %+v", reg.Switch)
	}

	for name, sw := range map[string]map[string]any{
		"unknown_level":  {"alice": "everything"},
		"bad_owner_name": {"Bad Owner": "catalog"},
		"empty_level":    {"alice": ""},
	} {
		saveYAML(t, filepath.Join(user, "models.yaml"), map[string]any{"switch": sw})
		if _, err := LoadModelRegistry(user, s); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestModelRegistryValidation(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	user := filepath.Join(spaces, "alice")
	if err := InitEnvironment(user, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]any{
		"provider_url_not_http":        {"providers": map[string]any{"p": map[string]any{"url": "ftp://x"}}},
		"provider_url_has_credentials": {"providers": map[string]any{"p": map[string]any{"url": "http://u:p@x.invalid"}}},
		"provider_name_invalid":        {"providers": map[string]any{"Bad Name": map[string]any{"url": "http://x.invalid"}}},
		"provider_key_env_lowercase":   {"providers": map[string]any{"p": map[string]any{"url": "http://x.invalid", "key_env": "lower_env"}}},
		"ladder_unknown_provider":      {"models": map[string]any{"m": map[string]any{"ladder": []any{map[string]any{"provider": "ghost", "model": "x"}}}}},
		"ladder_missing_model":         {"models": map[string]any{"m": map[string]any{"ladder": []any{map[string]any{"provider": "cliproxy"}}}}},
		"ladder_empty":                 {"models": map[string]any{"m": map[string]any{"ladder": []any{}}}},
		"model_owner_invalid":          {"models": map[string]any{"m": map[string]any{"owners": []string{"Bad Owner"}, "ladder": []any{map[string]any{"provider": "cliproxy", "model": "x"}}}}},
	}
	for name, doc := range cases {
		saveYAML(t, filepath.Join(spaces, "models.yaml"), doc)
		if _, err := LoadModelRegistry(user, s); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestModelProviderCredentialReferencesRequireValidOwnerGrants(t *testing.T) {
	for name, provider := range map[string]ModelProvider{
		"empty_grants":             {BrokerGrants: map[string]string{}, CredentialEnv: "OPENAI_API_KEY"},
		"invalid_owner":            {BrokerGrants: map[string]string{"Bad Owner": "grant-alice"}, CredentialEnv: "OPENAI_API_KEY"},
		"invalid_grant":            {BrokerGrants: map[string]string{"alice": "bad grant"}, CredentialEnv: "OPENAI_API_KEY"},
		"missing_target":           {BrokerGrants: map[string]string{"alice": "grant-alice"}},
		"invalid_target":           {BrokerGrants: map[string]string{"alice": "grant-alice"}, CredentialEnv: "lowercase"},
		"ambiguous_source":         {BrokerGrants: map[string]string{"alice": "grant-alice"}, CredentialEnv: "OPENAI_API_KEY", KeyEnv: "OWNER_KEY"},
		"invalid_owner_list":       {Owners: []string{"Bad Owner"}},
		"grant_outside_owner_list": {Owners: []string{"alice"}, BrokerGrants: map[string]string{"bob": "grant-bob"}, CredentialEnv: "OPENAI_API_KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			provider.URL = "https://provider.invalid"
			registry := ModelRegistry{Providers: map[string]ModelProvider{"p": provider}, Models: map[string]ModelEntry{"default": {Ladder: []ModelHopRef{{Provider: "p", Model: "reviewed"}}}}}
			if err := registry.Validate(); err == nil {
				t.Fatal("ambiguous or invalid credential scope accepted")
			}
		})
	}
}

func TestModelRegistryBoundsFallbackLadder(t *testing.T) {
	for _, count := range []int{8, 9} {
		hops := make([]ModelHopRef, count)
		for i := range hops {
			hops[i] = ModelHopRef{Provider: "cliproxy", Model: "reviewed"}
		}
		registry := ModelRegistry{Models: map[string]ModelEntry{"default": {Ladder: hops}}}
		err := registry.Validate()
		if count == 8 && err != nil {
			t.Errorf("bounded ladder rejected: %v", err)
		}
		if count == 9 && err == nil {
			t.Error("registry accepted more than eight fallback hops")
		}
	}
}

// A managed render materializes the merged registry into generated/ so the
// relay mounts it read-only, and provisions model-relay.auth for the owner
// switch endpoint.
func TestRenderEnvironmentWritesModelRegistry(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	if err := os.MkdirAll(spaces, 0700); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(spaces, "models.yaml"), map[string]any{
		"providers": map[string]any{
			"openai": map[string]any{"url": "https://api.openai.com/v1", "key_env": "OPENAI_KEY"},
		},
		"models": map[string]any{
			"default": map[string]any{"ladder": []any{
				map[string]any{"provider": "cliproxy", "model": "gemini-3-pro"},
				map[string]any{"provider": "openai", "model": "gpt-5"},
			}},
		},
	})
	user := filepath.Join(spaces, "alice")
	if err := InitEnvironment(user, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "managed", "p", 1
	s.Model, s.ModelURL = "m", "http://model-relay:8318/v1"
	if err := WriteSpace(user, s); err != nil {
		t.Fatal(err)
	}
	selection := `{"schema":1,"user":"alice","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(user, "execution.prod.json"), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(filepath.Join(user, "secrets.prod.env"), map[string]string{"OPENAI_API_KEY": "provider-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(filepath.Join(user, "model-relay.auth"), map[string]string{"HUB_MODEL_RELAY_ADMIN": "admin-fixture"}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte(`soul`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(user, root, "prod"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(user, "generated", "model-registry", "prod", "models.yaml"))
	if err != nil {
		t.Fatalf("registry not materialized: %v", err)
	}
	body := string(b)
	var registryView map[string]any
	if err := yaml.Unmarshal(b, &registryView); err != nil {
		t.Fatal(err)
	}
	if registryView["runtime_model"] != "m" {
		t.Error("rendered registry does not identify the runtime's pinned model alias")
	}
	if !strings.Contains(body, "openai") || !strings.Contains(body, "gpt-5") || !strings.Contains(body, "cliproxy") {
		t.Fatalf("merged registry missing layers: %s", body)
	}
	auth, err := os.ReadFile(filepath.Join(user, "model-relay.auth"))
	if err != nil || !strings.Contains(string(auth), "HUB_MODEL_RELAY_ADMIN=") {
		t.Fatalf("model-relay.auth not provisioned: %v", err)
	}
	relayAuth, err := ReadSecrets(filepath.Join(user, "model-relay.auth"))
	if err != nil {
		t.Fatal(err)
	}
	managed, err := ReadSecrets(filepath.Join(user, "managed-runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := ReadSecrets(filepath.Join(user, "runtime.prod.env"))
	if err != nil {
		t.Fatal(err)
	}
	if relayAuth["HUB_MODEL_RELAY_AUTH"] == "" || relayAuth["HUB_MODEL_RELAY_AUTH"] == relayAuth["HUB_MODEL_RELAY_ADMIN"] {
		t.Error("runtime relay bearer missing or equals admin bearer")
	}
	if relayAuth["HUB_MODEL_RELAY_ADMIN"] != "admin-fixture" {
		t.Error("existing admin bearer changed during migration")
	}
	if managed["OPENAI_API_KEY"] == "provider-fixture" || managed["OPENAI_API_KEY"] != relayAuth["HUB_MODEL_RELAY_AUTH"] {
		t.Error("runtime receives provider credential instead of its relay bearer")
	}
	if managed["HUB_MODEL_RELAY_ADMIN"] != "" || provider["OPENAI_API_KEY"] != "provider-fixture" {
		t.Error("credential ownership changed across relay and runtime")
	}
	if err := RenderEnvironment(user, root, "prod"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(user, "model-relay.auth"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(auth) {
		t.Error("rerender rotated relay auth without instruction")
	}
}

// A managed space with no models.yaml anywhere still renders a working
// registry: the space's cliproxy carrying the configured model.
func TestRenderEnvironmentSynthesizesDefaultRegistry(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	user := filepath.Join(spaces, "alice")
	if err := InitEnvironment(user, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.CapabilityMode, s.CapabilityProfileID, s.CapabilityGeneration = "managed", "p", 1
	s.Model, s.ModelURL = "m", "http://model-relay:8318/v1"
	if err := WriteSpace(user, s); err != nil {
		t.Fatal(err)
	}
	selection := `{"schema":1,"user":"alice","environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`
	if err := os.WriteFile(filepath.Join(user, "execution.prod.json"), []byte(selection), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte(`soul`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(user, root, "prod"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(user, "generated", "model-registry", "prod", "models.yaml"))
	if err != nil {
		t.Fatalf("default registry not materialized: %v", err)
	}
	if !strings.Contains(string(b), "cliproxy") || !strings.Contains(string(b), "model: m") {
		t.Fatalf("synthesized default missing: %s", b)
	}
}
