package stack

import (
	"context"
	"fmt"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
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
		publishManagedRevision(t, dir, owner, "r-0123456789abcdef")
		settings, err := ReadEnvironment(context.Background(), testReader(dir), dir, "prod")
		if err != nil {
			t.Fatal(err)
		}
		settings.CapabilityProfileID = "shared-model-profile"
		settings.CapabilityGeneration = 1
		settings.Model = "shared-model"
		settings.ModelURL = "http://model-relay:8318/v1"
		if err := saveSpace(t, dir, settings); err != nil {
			t.Fatal(err)
		}
		selection := fmt.Sprintf(`{"schema":1,"user":%q,"environment":"prod","mode":"supervisor","supervisor_url":"http://localhost:8876","native_cron":"disabled","compatibility_release":"0.3.0"}`, owner)
		if err := os.WriteFile(filepath.Join(dir, "execution.prod.json"), []byte(selection), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeEnvFile(filepath.Join(dir, "secrets.prod.env"), map[string]string{"OPENAI_API_KEY": owner + "-fixture"}); err != nil {
			t.Fatal(err)
		}
		if err := RenderEnvironment(context.Background(), testReader(dir), dir, root, "prod"); err != nil {
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
	if err := RenderEnvironment(context.Background(), testReader(alice), alice, root, "prod"); err != nil {
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

func TestModelRegistryRejectsUnpublishedLegacySources(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := InitEnvironment(dir, "alice", "prod"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(filepath.Dir(dir), "models.yaml"), filepath.Join(dir, "models.yaml")} {
		saveYAML(t, path, map[string]any{"models": map[string]any{"legacy": map[string]any{"ladder": []any{map[string]any{"provider": "cliproxy", "model": "legacy"}}}}})
	}
	if _, err := testReader(dir).Effective(context.Background(), spawnv1.Tenant{}, "alice"); err == nil {
		t.Fatal("retired model files fabricated an effective spec")
	}
	publishManagedRevision(t, dir, "alice", "r-model-current")
	registry, err := LoadModelRegistryFor(dir, testLoadEffective(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Models) != 1 || len(registry.Models["m"].Ladder) != 1 {
		t.Fatal("retired model file changed the published registry")
	}
	for _, pointer := range []string{"pending", "r-missing"} {
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(pointer), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := testReader(dir).Effective(context.Background(), spawnv1.Tenant{}, "alice"); err == nil {
			t.Fatal("unloadable revision fabricated an effective spec")
		}
	}
}

func TestModelRegistrySwitchLevels(t *testing.T) {
	for _, level := range []string{"none", "logical", "catalog"} {
		registry := ModelRegistry{Switch: map[string]string{"alice": level, "*": "logical"}}
		if err := registry.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range []map[string]string{{"alice": "everything"}, {"Bad Owner": "catalog"}, {"alice": ""}} {
		registry := ModelRegistry{Switch: values}
		if err := registry.Validate(); err == nil {
			t.Fatal("invalid model switch contract accepted")
		}
	}
}

func TestModelRegistryValidation(t *testing.T) {
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
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var registry ModelRegistry
		if err := yaml.Unmarshal(body, &registry); err != nil {
			t.Fatal(err)
		}
		if err := registry.Validate(); err == nil {
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
	publishManagedRevision(t, user, "alice", "r-0000000000000001")
	s, err := ReadEnvironment(context.Background(), testReader(user), user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.CapabilityProfileID, s.CapabilityGeneration = "p", 1
	s.Model, s.ModelURL = "m", "http://model-relay:8318/v1"
	if err := saveSpace(t, user, s); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["models"] = "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy, openai]}\n  vision: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, auth: {proxy_access: {kind: broker-key, ref: cliproxy}}, models: [{id: gemini-3-pro}]}\n  openai: {via: openai-compatible, endpoint: https://api.openai.com/v1, auth: {upstream: {kind: broker-key, ref: openai-key}}, models: [{id: gpt-5}]}\n"
	publishManagedDocs(t, user, "alice", "r-0123456789abcdef", docs)
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
	if err := RenderEnvironment(context.Background(), testReader(user), user, root, "prod"); err != nil {
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
	if managed["HUB_MEDIA_VISION_MODEL"] != "vision" {
		t.Error("published vision route was not selected for media inspection")
	}
	if managed["HUB_MODEL_RELAY_ADMIN"] != "" || provider["OPENAI_API_KEY"] != "provider-fixture" {
		t.Error("credential ownership changed across relay and runtime")
	}
	admin, err := ReadSecrets(filepath.Join(user, "model-relay-admin.auth"))
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 1 || admin["HUB_MODEL_RELAY_ADMIN"] != relayAuth["HUB_MODEL_RELAY_ADMIN"] {
		t.Error("ToolHub auth must contain only the relay admin credential")
	}
	if err := RenderEnvironment(context.Background(), testReader(user), user, root, "prod"); err != nil {
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
	publishManagedRevision(t, user, "alice", "r-0000000000000001")
	s, err := ReadEnvironment(context.Background(), testReader(user), user, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.CapabilityProfileID, s.CapabilityGeneration = "p", 1
	s.Model, s.ModelURL = "m", "http://model-relay:8318/v1"
	if err := saveSpace(t, user, s); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, user, "alice", "r-0123456789abcdef")
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
	if err := RenderEnvironment(context.Background(), testReader(user), user, root, "prod"); err != nil {
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
