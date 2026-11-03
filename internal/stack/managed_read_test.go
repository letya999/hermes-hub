package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCurrentPointer stamps generated/passports/current without publishing
// the revision it names — the managed marker alone must gate the read path.
func writeCurrentPointer(t *testing.T, dir, revision string) {
	t.Helper()
	passports := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, "current"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestBrokenPublishedRevisionFailsClosed pins the authority rule: once a
// space has a current pointer, a revision that cannot fully load is a hard
// error everywhere — never a silent downgrade to the legacy files.
func TestBrokenPublishedRevisionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\nmodel: legacy-model\nmodel_url: http://cliproxy:8317/v1\noauth_port: 8000\nbrowser_port: 6080\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\ntools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeCurrentPointer(t, dir, "r-doesnotexist")
	s, err := Read(dir)
	if err == nil {
		t.Fatalf("broken published revision silently downgraded to legacy read (model=%q)", s.Model)
	}
	if !strings.Contains(err.Error(), "published revision") && !strings.Contains(err.Error(), "load effective") {
		t.Fatalf("error does not name the failed published revision: %v", err)
	}
}

// managedDocs returns the smallest complete v2 doc set a managed render can
// drive end to end: identity, the supervised runtime plus the relays it
// asserts on, a cliproxy default route, one toolset and an empty grant map.
// Tests override entries before publishing to shape their assertions.
func managedDocs(user string) map[string]string {
	return map[string]string{
		"stack": "kind: stack\nidentity: {user: " + user + "}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
}

// publishManagedDocs writes the six docs of a managed revision with their
// envelope and points current at it.
func publishManagedDocs(t *testing.T, dir, user, revision string, docs map[string]string) {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", revision)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	env := "apiVersion: six-passport/v2\nmetadata: {space: " + user + ", environment: dev, revision: " + revision + ", generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\n"
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body, ok := docs[name]
		if !ok {
			t.Fatalf("managed revision missing %s.yaml", name)
		}
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(env+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCurrentPointer(t, dir, revision)
}

// publishManagedRevision publishes the default managed doc set.
func publishManagedRevision(t *testing.T, dir, user, revision string) {
	t.Helper()
	publishManagedDocs(t, dir, user, revision, managedDocs(user))
}

// TestManagedRenderRefusesWithoutRevision: a space that claims managed mode
// but carries no published revision refuses the runtime read path — it must
// migrate before it renders.
func TestManagedRenderRefusesWithoutRevision(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(dir, "dev"); err == nil {
		t.Fatal("managed claim without a published revision read cleanly")
	}
	if err := RenderEnvironment(dir, dir, "dev"); err == nil {
		t.Fatal("managed claim without a published revision rendered cleanly")
	}
}

// TestManagedRenderReadsNoLegacyFile: for a managed space the retired
// authority files are dead — poisoned settings/workspace/models content
// must never be opened, the revision's values win, and the agent.yaml
// extension fields the contract does not govern survive.
func TestManagedRenderReadsNoLegacyFile(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\ninfra: false\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	// Poison every retired file: strict-decode garbage on disk must never be
	// consulted — a read would fail, so success proves the file stayed shut.
	for _, name := range []string{"settings.yaml", "workspace.yaml", "models.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("managed read touched a retired file or failed: %v", err)
	}
	if s.Infra == nil || *s.Infra {
		t.Fatalf("extension field infra=false lost: %#v", s.Infra)
	}
	if s.Model != "m" || s.ModelURL != "http://model-relay:8318/v1" {
		t.Fatalf("passport model leaves not applied: model=%q url=%q", s.Model, s.ModelURL)
	}
	if !s.Has("files") {
		t.Fatalf("passport toolset surface lost: %v", s.Tools)
	}
}

// TestUnmanagedSpaceReadsLegacyLayout: a space with no revision and no
// managed claim keeps the legacy authority — the transition contract.
func TestUnmanagedSpaceReadsLegacyLayout(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\nmodel: legacy-model\nmodel_url: http://cliproxy:8317/v1\noauth_port: 8000\nbrowser_port: 6080\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\ntools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("unmanaged space refused the legacy read: %v", err)
	}
	if s.Model != "legacy-model" {
		t.Fatalf("legacy authority lost: model=%q", s.Model)
	}
}

// TestPointerAloneClaimsManaged: the published-revision pointer is itself
// the managed marker — an agent.yaml that never wrote capability_mode (an
// operator-edited extension file) still reads the revision only; a poisoned
// workspace.yaml on disk must never be opened.
func TestPointerAloneClaimsManaged(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("pointer-only managed space read a retired file or failed: %v", err)
	}
	if s.Model != "m" {
		t.Fatalf("passport default route lost: model=%q", s.Model)
	}
}

// TestManagedRenderIgnoresRetiredFiles: the render path itself resolves the
// managed space from its revision — a poisoned workspace.yaml left on disk
// must not fail the compose render.
func TestManagedRenderIgnoresRetiredFiles(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=k\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "config", "SOUL.md"), []byte("# soul\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, out, "dev"); err != nil {
		t.Fatalf("managed render consulted a retired file: %v", err)
	}
}

// TestManagedModelRegistryReadsNoLegacyLayer: under a published revision the
// relay's model registry comes from the models doc alone — a retired
// models.yaml still on disk must not contribute providers, routes or switch
// entries (SPEC-0092 R8.1).
func TestManagedModelRegistryReadsNoLegacyLayer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	agent := "schema: 3\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	legacy := "providers:\n  ghost: {url: http://ghost.invalid/v1}\nmodels:\n  ghost-route: {ladder: [{provider: ghost}]}\n"
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistry(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Providers["ghost"]; ok {
		t.Fatal("retired models.yaml leaked a provider into the managed registry")
	}
	if _, ok := reg.Models["ghost-route"]; ok {
		t.Fatal("retired models.yaml leaked a route into the managed registry")
	}
	if _, ok := reg.Providers["cliproxy"]; !ok {
		t.Fatal("passport provider lost from the managed registry")
	}
}

// TestMalformedCurrentPointerFailsClosed pins the marker rule: a present
// but non-local/traversal current id is still a published-authority claim —
// the read path refuses rather than treating the space as unmanaged.
func TestMalformedCurrentPointerFailsClosed(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\nmodel: legacy-model\nmodel_url: http://cliproxy:8317/v1\noauth_port: 8000\nbrowser_port: 6080\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\ntools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	passports := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, "current"), []byte("../escape\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("malformed current pointer silently read as an unmanaged space")
	}
}
