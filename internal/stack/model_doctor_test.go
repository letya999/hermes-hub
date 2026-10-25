package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelDoctorScopesProvidersAndCalculatesOwnerApplyPlan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "models.yaml")
	write := func(url string) {
		t.Helper()
		saveYAML(t, source, map[string]any{"providers": map[string]any{
			"account":   map[string]any{"url": url, "owners": []string{"alice"}, "key_env": "MODEL_KEY"},
			"broker":    map[string]any{"url": "https://broker-provider.invalid", "broker_grants": map[string]string{"alice": "grant-alice"}, "credential_env": "MODEL_API_KEY"},
			"anonymous": map[string]any{"url": "https://anonymous.invalid"},
			"foreign":   map[string]any{"url": "https://foreign.invalid", "owners": []string{"bob"}, "key_env": "BOB_KEY"},
		}, "models": map[string]any{"default": map[string]any{"ladder": []any{map[string]string{"provider": "account", "model": "model-a"}, map[string]string{"provider": "cliproxy", "model": "model-b"}}}}})
	}
	settings := Settings{User: "alice", Environment: "prod", Model: "logical", Cliproxy: "shared"}
	write("https://old.invalid/v1")
	if err := writeModelRegistry(dir, settings); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvFile(filepath.Join(dir, "runtime.prod.env"), map[string]string{"MODEL_KEY": "private-fixture-value", "OPENAI_API_KEY": "builtin-fixture"}); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(dir, "generated", "compose.prod.yaml"), map[string]any{"services": map[string]any{"model-relay": map[string]any{"environment": map[string]string{"HUB_MODEL_RELAY_CLIPROXY": "http://cliproxy:8317"}}}})
	check := func(expected []string, secrets map[string]string) {
		t.Helper()
		lines, err := ModelDoctor(dir, settings, secrets)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		for _, part := range expected {
			if !strings.Contains(text, part) {
				t.Errorf("model diagnostics omitted %q: %s", part, text)
			}
		}
		if strings.Contains(text, "private-fixture-value") || strings.Contains(text, "grant-alice") {
			t.Error("diagnostics exposed credential material/reference value")
		}
	}
	check([]string{"provider=account", "credential_scope=owner-env", "provider=broker", "credential_scope=owner-broker", "credential_scope=anonymous", "authorized=false", "registry unchanged", "credentials unchanged", "proxy unchanged", "runtime unchanged"}, map[string]string{"MODEL_KEY": "private-fixture-value", "OPENAI_API_KEY": "builtin-fixture", "BOB_KEY": "rotated-bob-fixture"})
	check([]string{"registry unchanged", "recreate owner model-relay", "proxy unchanged"}, map[string]string{"MODEL_KEY": "private-fixture-value", "OPENAI_API_KEY": "rotated-builtin-fixture"})
	write("https://new.invalid/v1")
	settings.Model = "new-logical"
	settings.Cliproxy = "dedicated"
	check([]string{"render registry; relay reloads", "recreate owner model-relay", "recreate owner proxy", "next owner runtime spawn"}, map[string]string{"MODEL_KEY": "rotated-fixture"})
	saveYAML(t, source, map[string]any{"providers": map[string]any{"cliproxy": map[string]any{"url": "https://broker-provider.invalid", "broker_grants": map[string]string{"alice": "grant-alice"}, "credential_env": "MODEL_API_KEY"}}, "models": map[string]any{"default": map[string]any{"ladder": []any{map[string]string{"provider": "cliproxy", "model": "model-a"}}}}})
	if err := writeModelRegistry(dir, settings); err != nil {
		t.Fatal(err)
	}
	check([]string{"credentials unchanged"}, map[string]string{"OPENAI_API_KEY": "unrelated-rotated-fixture"})
	if err := os.WriteFile(filepath.Join(dir, "generated", "model-registry", "prod", "models.yaml"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelDoctor(dir, settings, nil); err == nil {
		t.Error("doctor trusted invalid rendered registry")
	}
	if err := os.Remove(filepath.Join(dir, "generated", "model-registry", "prod", "models.yaml")); err != nil {
		t.Fatal(err)
	}
	check([]string{"registry not rendered"}, map[string]string{"MODEL_KEY": "rotated-fixture"})
	if err := os.WriteFile(filepath.Join(dir, "generated", "compose.prod.yaml"), []byte("["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelDoctor(dir, settings, nil); err == nil {
		t.Error("doctor trusted invalid rendered compose")
	}
	for _, name := range []string{"generated/compose.prod.yaml", "runtime.prod.env"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	check([]string{"proxy config not rendered", "provider env not rendered"}, nil)
	if err := os.WriteFile(source, []byte("unknown: fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelDoctor(dir, settings, nil); err == nil {
		t.Error("doctor accepted malformed registry")
	}
}
