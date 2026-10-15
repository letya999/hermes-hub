package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	b, err := os.ReadFile(filepath.Join(user, "generated", "models.prod.yaml"))
	if err != nil {
		t.Fatalf("registry not materialized: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, "openai") || !strings.Contains(body, "gpt-5") || !strings.Contains(body, "cliproxy") {
		t.Fatalf("merged registry missing layers: %s", body)
	}
	auth, err := os.ReadFile(filepath.Join(user, "model-relay.auth"))
	if err != nil || !strings.Contains(string(auth), "HUB_MODEL_RELAY_ADMIN=") {
		t.Fatalf("model-relay.auth not provisioned: %v", err)
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
	b, err := os.ReadFile(filepath.Join(user, "generated", "models.prod.yaml"))
	if err != nil {
		t.Fatalf("default registry not materialized: %v", err)
	}
	if !strings.Contains(string(b), "cliproxy") || !strings.Contains(string(b), "model: m") {
		t.Fatalf("synthesized default missing: %s", b)
	}
}
