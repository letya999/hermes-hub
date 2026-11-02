package migration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CompileSources reads the editable authority — the five passport-src
// layers — and produces a strictly decodable v2 revision without writing.
func TestCompileSourcesReadsLayersAndCompilesV2(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("schema: 3\nuser: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "passport-src")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	layers := map[string]string{
		"platform-ceiling.yaml": `name: generated-platform-ceiling
version: 1
docs:
  stack:
    identity: {user: alice}
    services:
      hermes-runtime: {mode: supervised}
      toolhub: {mode: shared}
      model-relay: {mode: shared}
      communication-hub: {mode: shared}
      credential-broker: {mode: shared}
      supervisor: {mode: shared}
      cliproxy: {mode: shared}
      browser: {mode: per-space}
  tools:
    core:
      native:file: {via: native}
      hub:files: {via: toolhub}
  workspace:
    writable: [/workspace]
    read_only: [/config]
  models:
    services:
      cliproxy: {via: cliproxy, endpoint: "http://cliproxy:8317/v1", auth: {proxy_access: {kind: broker-key, ref: cliproxy}}}
    routes:
      everyday: {ladder: [cliproxy]}
  grants:
    allow: [workspace, tools]
`,
		"user.yaml": `name: alice
version: 1
docs:
  config:
    runtime: {timezone: Europe/Berlin}
  tools:
    core:
      native:file: {via: native}
  models:
    default: everyday
`,
	}
	for name, body := range layers {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadDir(dir)
	rev, err := CompileSources(dir, "dev", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CompileSources: %v", err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		if len(rev.Docs[name]) == 0 {
			t.Fatalf("compiled revision missing %s", name)
		}
	}
	eff, err := rev.Effective()
	if err != nil {
		t.Fatalf("compiled revision does not decode as v2: %v", err)
	}
	if eff.Config.Runtime.Timezone != "Europe/Berlin" || eff.Models.Default != "everyday" {
		t.Fatalf("user layer lost: %+v", eff.Config.Runtime)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatal("CompileSources wrote files")
	}
}

func TestCompileSourcesMissingSrcFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("schema: 3\nuser: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A source-less space selects no toolsets and holds no RBAC rights —
	// R2.3 makes the empty allow a compile error rather than a publishable
	// zero-authority revision.
	if _, err := CompileSources(dir, "dev", time.Now()); err == nil {
		t.Fatal("absent passport-src must fail on empty grants allow")
	}
}
