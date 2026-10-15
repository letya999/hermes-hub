package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Runtime env rendering merges org+user secrets with a duplicate-key error,
// filters gateway-owned and omitted keys into the communication file, pins
// the shared ToolHub endpoint, and renders bounded helper files.
func TestRenderEnvHelpers(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Environment: "dev"}

	// writeRuntimeEnvFiles: duplicate user/org keys fail; gateway keys and
	// omissions leave the runtime file and land in the communication file.
	user := map[string]string{"OPENAI_API_KEY": "k1", "TELEGRAM_BOT_TOKEN": "tg", "FAL_KEY": "fal"}
	org := map[string]string{"ORG_KEY": "o1"}
	if err := writeRuntimeEnvFiles(dir, "dev", user, org, []string{"FAL_KEY"}, map[string]string{"EXTRA_KEY": "x"}); err != nil {
		t.Fatal(err)
	}
	runtime, err := ReadSecrets(filepath.Join(dir, "runtime.dev.env"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime["OPENAI_API_KEY"] != "k1" || runtime["ORG_KEY"] != "o1" || runtime["EXTRA_KEY"] != "x" {
		t.Fatalf("runtime env: %v", runtime)
	}
	if _, ok := runtime["TELEGRAM_BOT_TOKEN"]; ok {
		t.Fatal("gateway secret leaked into runtime env")
	}
	if _, ok := runtime["FAL_KEY"]; ok {
		t.Fatal("omitted key leaked into runtime env")
	}
	if runtime["HUB_TOOLHUB_ENDPOINT"] != "http://toolhub:8090/mcp" {
		t.Fatalf("toolhub endpoint: %v", runtime)
	}
	gateway, err := ReadSecrets(filepath.Join(dir, "communication.dev.env"))
	if err != nil {
		t.Fatal(err)
	}
	if gateway["TELEGRAM_BOT_TOKEN"] != "tg" {
		t.Fatalf("gateway env: %v", gateway)
	}
	dup := map[string]string{"SHARED": "a"}
	if err := writeRuntimeEnvFiles(dir, "dev", dup, map[string]string{"SHARED": "b"}, nil, nil); err == nil {
		t.Fatal("duplicate org/user key accepted")
	}

	// Key enumeration: self keys exclude gateway-owned secrets; protected keys
	// are sorted and complete; configured keys skip empty values.
	for _, key := range runtimeSelfEnvKeys(s) {
		if GatewayOwnedSecret(key) {
			t.Fatalf("gateway secret %s in self keys", key)
		}
	}
	protected := runtimeProtectedEnvKeys(s)
	if !slices.IsSorted(protected) || !slices.Contains(protected, "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("protected keys: %v", protected)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("A_KEY=1\nEMPTY=\n# c\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if keys := configuredEnvKeys(s, dir); !slices.Equal(keys, []string{"A_KEY"}) {
		t.Fatalf("configured keys: %v", keys)
	}

	// Managed egress hosts: toolhub-backed tools map to fixed siblings, native
	// web contributes provider hosts; non-managed mode renders nothing.
	managed := Settings{CapabilityMode: "managed", Environment: "dev", Tools: map[string]ToolEntry{
		"routines": {Via: ToolViaToolHub},
		"hh":       {Via: ToolViaToolHub},
		"web":      {Via: ToolViaNative},
	}}
	hosts := managedEgressHosts(managed, dir)
	for _, want := range []string{"communication-hub", "api.hh.ru"} {
		if !slices.Contains(hosts, want) {
			t.Fatalf("egress hosts %v missing %s", hosts, want)
		}
	}
	if h := managedEgressHosts(Settings{}, dir); h != nil {
		t.Fatalf("non-managed egress: %v", h)
	}

	// Small file helpers: atomic env file write, honcho config gating,
	// removeIfExists semantics.
	out := filepath.Join(dir, "out.env")
	if err := writeEnvFile(out, map[string]string{"B": "2", "A": "1"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(out)
	if string(body) != "A=1\nB=2\n" {
		t.Fatalf("env file body: %q", body)
	}
	if err := writeHonchoConfig(dir, s); err != nil {
		t.Fatal(err)
	}
	honchoDir := filepath.Join(dir, "hermes")
	if err := os.MkdirAll(honchoDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeHonchoConfig(dir, Settings{Honcho: true, HonchoURL: "http://honcho:8000", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(honchoDir, "honcho.json")); err != nil || !strings.Contains(string(b), "honcho:8000") {
		t.Fatalf("honcho.json: %v", err)
	}
	if err := removeIfExists(filepath.Join(dir, "nope")); err != nil {
		t.Fatal(err)
	}
	if err := removeIfExists(out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
}
