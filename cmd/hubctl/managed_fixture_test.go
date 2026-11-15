package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
)

// publishAuthored compiles the space's authored passports and publishes the
// revision — the real CompileAuthored → Publish path `space apply` takes,
// short of the reconcile handlers that need a live Docker daemon.
func publishAuthored(t *testing.T, dir, env string) {
	t.Helper()
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := passport.CompileAuthored(dir, env, time.Now(), catalogDocs, stack.ReservedSiblingPorts(dir), map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(dir); err != nil {
		t.Fatal(err)
	}
}

// writeManagedSpaceFixture installs a migrated managed space: a published
// six-document revision under generated/passports — no retired
// settings/workspace/models/agent/scope files at all (SPEC-0095).
func writeManagedSpaceFixture(t *testing.T, dir, user string) {
	t.Helper()
	writeManagedSpaceDocs(t, dir, user, nil)
}

// managedInfraDocs renders an infra space: every shared service declared,
// plus — when withGateway — a telegram channel so the shared gateway has a
// reason to render (the plan check refuses a declared-but-unrendered
// communication-hub).
func managedInfraDocs(user string, withGateway bool) map[string]string {
	services := "  hermes-runtime: {mode: supervised}\n" +
		"  toolhub: {mode: shared}\n" +
		"  model-relay: {mode: shared}\n" +
		"  cliproxy: {mode: shared}\n" +
		"  credential-broker: {mode: shared}\n" +
		"  supervisor: {mode: shared}\n"
	config := "kind: config\nruntime: {timezone: UTC}\nmemory: {enabled: false}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"
	if withGateway {
		services += "  communication-hub: {mode: shared}\n"
		config += "channels:\n  telegram: {}\n"
	}
	return map[string]string{
		"stack":     "kind: stack\nidentity: {user: " + user + ", infra: true}\nservices:\n" + services,
		"config":    config,
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
}

// writeManagedSpaceDocs installs the fixture with per-doc overrides keyed by
// document name (stack/config/models/workspace/tools/grants).
func writeManagedSpaceDocs(t *testing.T, dir, user string, overrides map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// No agent.yaml/scope.yaml stubs: the published revision alone must
	// carry every consumer (SPEC-0095) — retired files are not an input.
	rev := "r-0123456789abcdef"
	revDir := filepath.Join(dir, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	env := "apiVersion: six-passport/v2\nmetadata: {space: " + user + ", environment: dev, revision: " + rev + ", generated_at: 2026-11-02T00:00:00Z}\n"
	docs := map[string]string{
		"stack": "kind: stack\nidentity: {user: " + user + ", infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [upstream]}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
	for name, body := range overrides {
		docs[name] = body
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		if name == "tools" && !strings.Contains(docs[name], "binding:") {
			docs[name] += "binding: {profile_id: " + user + "-default, generation: 1}\n"
		}
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(env+docs[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(dir, "generated", "config-view", "dev")
	if err := os.MkdirAll(view, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "revision"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestUpReadsManagedRevision: `hubctl up` resolves the space through the
// published revision — a migrated space has no legacy authority files left
// to read (SPEC-0092 R8.1).
func TestUpReadsManagedRevision(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"up", "--dir", dir, "--root", "../..", "--no-build"}); err != nil {
		t.Fatal(err)
	}
	// The build leg resolves the same space for the agent-image decision.
	if err := run(context.Background(), []string{"up", "--dir", dir, "--root", "../.."}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "up -d") {
		t.Fatalf("managed up did not reach compose up: %s", out)
	}
}

// `hubctl chat` reads the space's feature surface through the same reader —
// on a migrated space the passport decides whether CLI chat is allowed.
func TestChatReadsManagedRevision(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HUB_TEST_LOG\"\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\n>>\"%HUB_TEST_LOG%\" echo %*\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HUB_TEST_LOG", log)
	if err := run(context.Background(), []string{"chat", "--dir", dir, "--root", "../.."}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "exec") || !strings.Contains(string(out), "chat") {
		t.Fatalf("managed chat did not reach docker exec: %s", out)
	}
}

// Trace resolves each context's Hermes home through the managed-aware read:
// a migrated space keeps its logs under managed/<env>/hermes.
func TestRunTraceReadsManagedRevision(t *testing.T) {
	previous := traceDockerNames
	traceDockerNames = func(context.Context, string, string, string) ([]string, error) { return nil, nil }
	t.Cleanup(func() { traceDockerNames = previous })
	root := t.TempDir()
	spaces := filepath.Join(root, "spaces")
	if err := os.MkdirAll(filepath.Join(spaces, ".control"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(spaces, "alice")
	writeManagedSpaceFixture(t, dir, "alice")
	if err := os.MkdirAll(filepath.Join(dir, "managed", "prod", "hermes", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".local", "diagnostics"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"jobs":{"job-1":{"request":{"principal_id":"alice"},"response":{"run_id":"run-9","session_id":"sess-4"},"status":"completed"}}}`
	if err := os.WriteFile(filepath.Join(spaces, ".control", "supervisor.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisorLog := filepath.Join(root, ".local", "diagnostics", "supervisor.log")
	if err := os.WriteFile(supervisorLog, []byte("{\"time\":\"2026-01-02T03:04:01Z\",\"component\":\"supervisor\",\"msg\":\"admitted\",\"job_id\":\"job-1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "managed", "prod", "hermes", "logs", "agent.log"), []byte("2026-01-02T03:04:03Z run-9 reply sess-4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runTrace(context.Background(), &out, "job-1", "", spaces); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "space/agent.log") {
		t.Fatalf("managed home log missing from trace: %q", out.String())
	}
}

// A host-side skill publish refreshes the agent-visible view from the space
// settings — on a migrated space those settings come from the revision, so
// refresh must resolve them rather than warn-and-skip.
func TestSkillInstallRefreshesEffectiveViewOnManagedRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	md := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(md, []byte("---\nname: probe\ndescription: d\n---\n\nBody.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"skill", "install", "--dir", dir, "--user", "dana", "--name", "probe", "--from-file", md, "--consent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stack.GeneratedEffectiveSkillsDir(dir), "probe", "SKILL.md")); err != nil {
		t.Fatalf("consent-installed skill missing from the effective view: %v", err)
	}
}

// `skill promote` reads the space's org binding from settings — on a migrated
// space that read must resolve through the revision before the org gate runs.
func TestSkillPromoteReadsManagedRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	err := run(context.Background(), []string{"skill", "promote", "--dir", dir, "--user", "dana", "--id", "draft-1"})
	if err == nil || !strings.Contains(err.Error(), "not bound to an organization skills store") {
		t.Fatalf("promote must reach the org-binding gate, got: %v", err)
	}
}

// `dev-rebuild` resolves the dev stack through the space settings — on a
// migrated space that read must come from the revision (later render/docker
// stages may legitimately fail without a daemon).
func TestDevRebuildReadsManagedRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	err := run(context.Background(), []string{"dev-rebuild", "--dir", dir, "--env", "dev", "model-relay"})
	if err != nil && strings.Contains(err.Error(), "capability") {
		t.Fatalf("dev-rebuild never reached the stack stages: %v", err)
	}
}

// Executor selection validates the caller against the space identity — the
// migrated space's identity comes from the revision, not a legacy file.
func TestSelectExecutionReadsManagedRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "dana")
	writeManagedSpaceFixture(t, dir, "dana")
	spool := t.TempDir()
	for _, sub := range []string{"pending", "running", "done", "failed", "outbox/pending", "outbox/sending", "outbox/failed"} {
		if err := os.MkdirAll(filepath.Join(spool, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	selection := stack.ExecutionSelection{Schema: 1, User: "dana", Environment: "dev", Mode: "supervisor", SupervisorURL: "http://supervisor:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	if err := selectExecution(context.Background(), dir, t.TempDir(), spool, selection, "auth", "img", false); err != nil {
		t.Fatal(err)
	}
}
