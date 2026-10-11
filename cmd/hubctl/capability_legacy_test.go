package main

import (
	"github.com/letya999/hermes-hub/internal/stack"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityToolsMigratesLegacyAtomically(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.yaml")
	original := []byte("# operator comment\nschema: 1\nuser: alice\nmodel: test\noauth_port: 8000\nbrowser_port: 6080\nfeatures: [workspace, browser, telegram]\nnative_toolsets: [todo]\ndisabled_mcp: [search]\n")
	if err := os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapabilityTools(p, "terminal=toolhub", true); err != nil {
		t.Fatal(err)
	}
	s, err := stack.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tools["terminal"].Via != stack.ToolViaToolHub || s.Tools["browser"].Via != stack.ToolViaMCP || s.Tools["todo"].Via != stack.ToolViaNative || s.Tools["search"].Via != stack.ToolViaOff || len(s.Ingress) == 0 {
		t.Fatalf("legacy wishes lost: %+v", s)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# operator comment") || strings.Contains(string(b), "features:") {
		t.Fatal("legacy fields or comment lost")
	}
	if err := runCapabilityTools(p, "terminal=unknown", true); err == nil {
		t.Fatal("bad mutation accepted")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(b) {
		t.Fatal("failed mutation changed file")
	}
}

func TestCapabilityToolsRejectsBrokenFilesWithoutWriting(t *testing.T) {
	for _, body := range []string{"[", "scalar", "schema: 1\nuser: alice\nfeatures: [unknown]\n", "schema: 1\ntools: invalid\n", "schema: 1\ntools: {}\n"} {
		p := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := writeToolEntries(p, true, map[string]stack.ToolEntry{"terminal": {Via: stack.ToolViaToolHub}}, nil); err == nil {
			t.Fatal("broken legacy settings accepted")
		}
		after, _ := os.ReadFile(p)
		if string(after) != body {
			t.Fatal("broken settings overwritten")
		}
	}
	if err := writeToolEntries(filepath.Join(t.TempDir(), "missing"), true, nil, nil); err == nil {
		t.Fatal("missing target accepted")
	}
	for _, body := range []string{"tools: invalid", "tools: {}"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "workspace.yaml")
		_ = os.WriteFile(p, []byte(body), 0600)
		if err := writeToolEntries(p, false, map[string]stack.ToolEntry{"terminal": {Via: stack.ToolViaToolHub}}, nil); err == nil {
			t.Fatal("broken split settings accepted")
		}
		after, _ := os.ReadFile(p)
		if string(after) != body {
			t.Fatal("split settings overwritten")
		}
		_ = os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("broken: true"), 0600)
		if err := writeToolEntries(p, false, nil, nil); err == nil {
			t.Fatal("broken paired agent accepted")
		}
	}
	if err := runCapabilityTools(filepath.Join(t.TempDir(), "missing"), "terminal=toolhub", true); err == nil {
		t.Fatal("missing space accepted")
	}
	if err := runCapabilityTools("", "terminal=toolhub", true); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityToolsResolvesOnlySettingsTargets(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{dir, filepath.Join(dir, "workspace.yaml"), filepath.Join(dir, "agent.yaml"), filepath.Join(dir, "no.txt")} {
		if _, _, err := workspaceTarget(p); err == nil {
			t.Fatalf("missing or unknown target accepted: %s", p)
		}
	}
	legacy := filepath.Join(dir, "settings.yaml")
	_ = os.WriteFile(legacy, []byte("schema: 1"), 0600)
	for _, p := range []string{dir, filepath.Join(dir, "agent.yaml"), legacy, filepath.Join(dir, "custom.yml")} {
		target, isLegacy, err := workspaceTarget(p)
		if err != nil || !isLegacy || target == "" {
			t.Fatalf("legacy target: %s %v", p, err)
		}
	}
	workspace := filepath.Join(dir, "workspace.yaml")
	_ = os.WriteFile(workspace, []byte("schema: 3"), 0600)
	for _, p := range []string{dir, filepath.Join(dir, "agent.yaml"), workspace} {
		target, isLegacy, err := workspaceTarget(p)
		if err != nil || isLegacy || target != workspace {
			t.Fatalf("split target: %s %v", p, err)
		}
	}
	if err := writeAtomic(dir, []byte("must not replace directory")); err == nil {
		t.Fatal("directory replaced by settings")
	}
	if err := writeAtomic(filepath.Join(dir, "missing", "workspace.yaml"), nil); err == nil {
		t.Fatal("missing parent accepted")
	}
}
