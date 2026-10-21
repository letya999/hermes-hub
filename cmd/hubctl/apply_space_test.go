package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

func initTestSpace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "me")
	if err := stack.InitEnvironment(dir, "me", "prod"); err != nil {
		t.Fatalf("init space: %v", err)
	}
	return dir
}

func TestApplySpaceInvalidWritesNothing(t *testing.T) {
	spaceDir := initTestSpace(t)
	spool := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "m.yaml")
	body := []byte(`space:
  workspace:
    bogus_key: 1
`)
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"agent.yaml", "workspace.yaml"} {
		b, err := os.ReadFile(filepath.Join(spaceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = b
	}
	if _, err := applyFile(spool, "", "me", manifest, spaceDir); err == nil {
		t.Fatal("expected invalid space manifest to fail")
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(spaceDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s mutated by invalid apply", name)
		}
	}
}

func TestApplySpaceRejectsSecrets(t *testing.T) {
	spaceDir := initTestSpace(t)
	for _, doc := range []string{
		"secrets: {token: x}",
		"tools:\n      file: {via: native, api_key: x}",
		"mcp:\n      srv: {url: 'https://x', bot_token: x}",
	} {
		spool := t.TempDir()
		manifest := filepath.Join(t.TempDir(), "m.yaml")
		body := []byte("space:\n  workspace:\n    " + strings.ReplaceAll(doc, "\n", "\n    ") + "\n")
		if err := os.WriteFile(manifest, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyFile(spool, "", "me", manifest, spaceDir); err == nil {
			t.Fatalf("expected secret-key rejection for %q", doc)
		}
	}
}

func TestApplySpaceConverges(t *testing.T) {
	spaceDir := initTestSpace(t)
	spool := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "m.yaml")
	agent, err := os.ReadFile(filepath.Join(spaceDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	indented := "    " + strings.ReplaceAll(strings.TrimSpace(string(agent)), "\n", "\n    ")
	body := []byte(`space:
  agent:
` + indented + `
  workspace:
    schema: 3
    tools:
      file: {via: toolhub, paths: [docs]}
`)
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := applyFile(spool, "", "me", manifest, spaceDir)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Space["workspace.yaml"] != "updated" && rep.Space["workspace.yaml"] != "created" {
		t.Fatalf("workspace.yaml not converged: %+v", rep)
	}
	got, err := stack.Read(spaceDir)
	if err != nil {
		t.Fatalf("converged space unreadable: %v", err)
	}
	if got.Tools["file"].Via != "toolhub" {
		t.Fatalf("tool entry lost: %+v", got.Tools)
	}
	if _, err := os.Stat(filepath.Join(spaceDir, "workspace.yaml")); err != nil {
		t.Fatal(err)
	}
	rep2, err := applyFile(spool, "", "me", manifest, spaceDir)
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	for name, action := range rep2.Space {
		if action != "unchanged" {
			t.Fatalf("re-apply must be idempotent, %s=%s", name, action)
		}
	}
	if !rep2.Converged {
		t.Fatalf("all-unchanged apply must report converged")
	}
}

func TestApplyVersionGate(t *testing.T) {
	spool := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "m.yaml")
	if err := os.WriteFile(manifest, []byte("version: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spool, "", "me", manifest, ""); err == nil {
		t.Fatal("version 3 manifest must be rejected")
	}
	if err := os.WriteFile(manifest, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFile(spool, "", "me", manifest, ""); err != nil {
		t.Fatalf("version 2 manifest must be accepted: %v", err)
	}
}

func TestApplySpaceDispatchDir(t *testing.T) {
	spaceDir := initTestSpace(t)
	spool := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "m.yaml")
	body := []byte(`space:
  workspace:
    schema: 3
    tools:
      file: {via: toolhub, paths: [docs]}
`)
	if err := os.WriteFile(manifest, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"apply", "-f", manifest, "--spool", spool, "--dir", spaceDir}); err != nil {
		t.Fatalf("apply --dir: %v", err)
	}
	if _, err := stack.Read(spaceDir); err != nil {
		t.Fatalf("space unreadable after apply: %v", err)
	}
}
