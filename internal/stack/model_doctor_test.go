package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
)

func TestDoctorUsesPublishedModelGuidance(t *testing.T) {
	settings := Settings{SpaceDir: t.TempDir()}
	issues := strings.Join(Doctor(settings, nil), "\n")
	for _, key := range []string{"doctor.model-default", "doctor.model-endpoint"} {
		if !strings.Contains(issues, locale.T(locale.LangOf(settings.SpaceDir), key)) {
			t.Fatalf("doctor omitted %s", key)
		}
	}
	if strings.Contains(issues, "agent.yaml") || strings.Contains(issues, "set model_url") {
		t.Fatal("doctor directs edits to retired model configuration")
	}
}

func TestModelRegistryReadableByRelayUID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bind permission contract")
	}
	dir := t.TempDir()
	publishManagedRevision(t, dir, "alice", "r-permissions")
	settings := Settings{User: "alice", Environment: "prod", Model: "logical", Cliproxy: "shared"}
	for range 2 {
		if err := writeModelRegistry(dir, settings); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(dir, "generated", "model-registry", "prod")
		for path, want := range map[string]os.FileMode{root: 0755, filepath.Join(root, "models.yaml"): 0644} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("relay bind permissions: %s want=%o err=%v", filepath.Base(path), want, err)
			}
		}
	}
}

func TestControllerConfigReadableByServiceUID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bind permission contract")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeToolHubFiles(dir, Settings{User: "alice", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"generic-controller.json": 0644, "generic-controller.key": 0640} {
		info, err := os.Stat(filepath.Join(dir, "runtime", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s: mode=%v want=%o", name, info.Mode().Perm(), want)
		}
	}
}

func TestSupervisorEnvFileReadableByServiceGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bind permission contract")
	}
	path := filepath.Join(t.TempDir(), "managed-runtime.dev.env")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := allowServiceGroupRead(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("supervisor cannot read env file: %v", err)
	}
}

func TestModelDoctorScopesProvidersAndCalculatesOwnerApplyPlan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	settings := Settings{User: "alice", Environment: "prod", Model: "logical", Cliproxy: "shared"}
	sequence := 0
	write := func(endpoint string, brokerProxy bool) {
		t.Helper()
		sequence++
		proxyAuth := ""
		if brokerProxy {
			proxyAuth = "auth: {proxy_access: {kind: broker-key, ref: grant-proxy}}"
		}
		docs := managedDocs("alice")
		docs["models"] = fmt.Sprintf(`kind: models
default: %s
routes:
  %s: {ladder: [account, cliproxy]}
services:
  account: {via: openai-compatible, endpoint: %q, models: [{id: model-a}]}
  broker: {via: openai-compatible, endpoint: https://broker-provider.invalid, auth: {upstream: {kind: broker-key, ref: grant-alice}}, models: [{id: model-b}]}
  anonymous: {via: openai-compatible, endpoint: https://anonymous.invalid, models: [{id: model-c}]}
  cliproxy:
    via: cliproxy
    endpoint: http://cliproxy:8317/v1
    models: [{id: model-d}]
    %s
`, settings.Model, settings.Model, endpoint, proxyAuth)
		publishManagedDocs(t, dir, "alice", fmt.Sprintf("r-doctor-%d", sequence), docs)
	}
	write("https://old.invalid/v1", false)
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
	check([]string{"provider=account", "provider=broker", "credential_scope=owner-broker", "credential_scope=anonymous", "registry unchanged", "credentials unchanged", "proxy unchanged", "runtime unchanged"}, map[string]string{"MODEL_KEY": "private-fixture-value", "OPENAI_API_KEY": "builtin-fixture", "BOB_KEY": "rotated-bob-fixture"})
	check([]string{"registry unchanged", "recreate owner model-relay", "proxy unchanged"}, map[string]string{"MODEL_KEY": "private-fixture-value", "OPENAI_API_KEY": "rotated-builtin-fixture"})
	settings.Model = "new-logical"
	write("https://new.invalid/v1", false)
	settings.Cliproxy = "dedicated"
	check([]string{"render registry; relay reloads", "recreate owner model-relay", "recreate owner proxy", "next owner runtime spawn"}, map[string]string{"MODEL_KEY": "rotated-fixture"})
	write("https://broker-provider.invalid/v1", true)
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
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte("unknown: fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelDoctor(dir, settings, nil); err != nil {
		t.Fatal("retired registry influenced model doctor:", err)
	}
	current := filepath.Join(dir, "generated", "passports", fmt.Sprintf("r-doctor-%d", sequence), "models.yaml")
	if err := os.WriteFile(current, []byte("unknown: fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ModelDoctor(dir, settings, nil); err == nil {
		t.Fatal("doctor accepted an invalid published model revision")
	}
}
