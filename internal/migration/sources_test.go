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
      files: {via: toolhub}
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
		names := []string{}
		for _, e := range after {
			names = append(names, e.Name())
		}
		t.Fatalf("CompileSources wrote files: %v (before %d)", names, len(before))
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

// A port a sibling space already published is reserved (SPEC-0092 R4.1):
// space apply must see the claim at compile, not at docker-proxy bind.
func TestCompileSourcesRejectsSiblingClaimedPort(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	sibling := filepath.Join(spaces, "bob")
	revDir := filepath.Join(sibling, "generated", "passports", "r-bob")
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	pub := "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: bob, environment: dev, revision: r-bob, generated_at: 2026-11-02T00:00:00Z, template: p@1, catalogs: \"4\"}\nports: {oauth-callback: 8100}\n"
	if err := os.WriteFile(filepath.Join(revDir, "config.yaml"), []byte(pub), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "generated", "passports", "current"), []byte("r-bob"), 0600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(spaces, "carol")
	src := filepath.Join(dir, "passport-src")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	layers := map[string]string{
		"platform-ceiling.yaml": `name: generated-platform-ceiling
version: 1
docs:
  stack:
    identity: {user: carol}
    services:
      hermes-runtime: {mode: supervised}
      toolhub: {mode: shared}
      model-relay: {mode: shared}
      communication-hub: {mode: shared}
      credential-broker: {mode: shared}
      supervisor: {mode: shared}
      cliproxy: {mode: shared}
  tools:
    core:
      files: {via: toolhub}
  workspace:
    writable: [/workspace]
  models:
    services:
      cliproxy: {via: cliproxy, endpoint: "http://cliproxy:8317/v1", auth: {proxy_access: {kind: broker-key, ref: cliproxy}}}
    routes:
      everyday: {ladder: [cliproxy]}
  grants:
    allow: [workspace, tools]
`,
		"user.yaml": `name: carol
version: 1
docs:
  config:
    ports: {oauth-callback: 8100}
`,
	}
	for name, body := range layers {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CompileSources(dir, "dev", time.Now()); err == nil {
		t.Fatal("sibling-published port compiled")
	}
}

// slack-events is the shared communication-hub ingress port — only the space
// that renders the shared infrastructure may declare it (SPEC-0092 R4.1).
func TestCompileSourcesRejectsSlackEventsForNonInfra(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("schema: 3\nuser: carol\ninfra: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "passport-src")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	body := `name: generated-platform-ceiling
version: 1
docs:
  stack:
    identity: {user: carol}
    services:
      hermes-runtime: {mode: supervised}
      toolhub: {mode: shared}
      model-relay: {mode: shared}
      communication-hub: {mode: shared}
      credential-broker: {mode: shared}
      supervisor: {mode: shared}
      cliproxy: {mode: shared}
  config:
    ports: {slack-events: 8088}
  tools:
    core:
      files: {via: toolhub}
  workspace:
    writable: [/workspace]
  models:
    services:
      cliproxy: {via: cliproxy, endpoint: "http://cliproxy:8317/v1", auth: {proxy_access: {kind: broker-key, ref: cliproxy}}}
    routes:
      everyday: {ladder: [cliproxy]}
  grants:
    allow: [workspace, tools]
`
	if err := os.WriteFile(filepath.Join(src, "platform-ceiling.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileSources(dir, "dev", time.Now()); err == nil {
		t.Fatal("non-infra space compiled slack-events")
	}
}

// The space that renders the shared infrastructure (no infra flag, or
// infra: true) is the one allowed to publish slack-events.
func TestCompileSourcesAllowsSlackEventsForInfra(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("schema: 3\nuser: local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "passport-src")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	body := `name: generated-platform-ceiling
version: 1
docs:
  stack:
    identity: {user: local}
    services:
      hermes-runtime: {mode: supervised}
      toolhub: {mode: shared}
      model-relay: {mode: shared}
      communication-hub: {mode: shared}
      credential-broker: {mode: shared}
      supervisor: {mode: shared}
      cliproxy: {mode: shared}
  config:
    ports: {slack-events: 8088}
  tools:
    core:
      files: {via: toolhub}
  workspace:
    writable: [/workspace]
  models:
    services:
      cliproxy: {via: cliproxy, endpoint: "http://cliproxy:8317/v1", auth: {proxy_access: {kind: broker-key, ref: cliproxy}}}
    routes:
      everyday: {ladder: [cliproxy]}
  grants:
    allow: [workspace, tools]
`
	if err := os.WriteFile(filepath.Join(src, "platform-ceiling.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileSources(dir, "dev", time.Now()); err != nil {
		t.Fatalf("infra space must compile slack-events: %v", err)
	}
}
