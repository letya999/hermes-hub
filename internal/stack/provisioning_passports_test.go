package stack_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestInitCreatesSixAuthoredPassports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := stack.InitEnvironment(dir, "alice", "dev"); err != nil {
		t.Fatal(err)
	}
	docs, err := passport.ReadAuthored(dir)
	if err != nil || len(docs) != 6 {
		t.Fatalf("init did not create six authored passports: count=%d err=%v", len(docs), err)
	}
	if docs["stack"]["identity"].(map[string]any)["user"] != "alice" {
		t.Fatal("init did not bind stack identity to user")
	}
	selected := docs["tools"]
	for _, family := range []string{"memory", "media", "sessions", "web_search"} {
		section := "extended"
		if family == "memory" {
			section = "core"
		}
		entry, ok := selected[section].(map[string]any)[family].(map[string]any)
		if !ok || entry["via"] != "toolhub" {
			t.Fatalf("default %s toolset must route through ToolHub: %v", family, entry)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	models := "default: everyday\nroutes: {everyday: {ladder: [upstream]}}\nservices:\n  upstream: {via: cliproxy, endpoint: 'http://cliproxy:8317/v1', auth: {proxy_access: {kind: broker-key, ref: cliproxy-default}}, models: [{id: everyday}]}\n"
	if err := os.WriteFile(filepath.Join(dir, "passports", "models.yaml"), []byte(models), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := passport.CompileAuthored(dir, "dev", time.Now().UTC(), catalogDocs, nil, map[string][]string{}); err != nil {
		t.Fatalf("filled init passport template does not compile: %v", err)
	}
}

// A clean init materializes only authored configuration: the six passports,
// never a retired agent/workspace/settings/scope file (ADR-0096 D2). A
// group space carries its scope in stack.identity.kind.
func TestInitWritesOnlyAuthoredPassports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "team")
	if err := stack.InitGroupEnvironment(dir, "team", "dev", ""); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"agent.yaml", "workspace.yaml", "settings.yaml", "scope.yaml"} {
		if _, err := os.Lstat(filepath.Join(dir, legacy)); err == nil {
			t.Fatalf("init wrote retired %s", legacy)
		}
	}
	docs, err := passport.ReadAuthored(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := docs["stack"]["identity"].(map[string]any)
	if identity["kind"] != "group" {
		t.Fatalf("group init did not stamp identity.kind: %v", identity)
	}
}

func TestInitUsesProfilePassportOverride(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	profile := filepath.Join(root, "policies", "profiles", "custom", "passports")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "config.yaml"), []byte("runtime: {timezone: Europe/Moscow}\nmemory: {enabled: false}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stack.InitEnvironmentProvision(dir, "alice", "dev", "custom"); err != nil {
		t.Fatal(err)
	}
	docs, err := passport.ReadAuthored(dir)
	if err != nil {
		t.Fatal(err)
	}
	if docs["config"]["runtime"].(map[string]any)["timezone"] != "Europe/Moscow" {
		t.Fatalf("profile passport override lost: %v", docs["config"])
	}
}

func TestInitDoesNotOverwriteExistingAuthoredPassport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	passports := filepath.Join(dir, "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(passports, "stack.yaml")
	if err := os.WriteFile(path, []byte("owner-edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stack.InitEnvironment(dir, "alice", "dev"); err == nil {
		t.Fatal("init overwrote an existing passport directory")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "owner-edited\n" {
		t.Fatalf("existing authored passport changed: %q, %v", body, err)
	}
}
