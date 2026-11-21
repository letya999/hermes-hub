package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM10KnowledgeClientProvenanceSearchAndLimits(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	v := fixture(t)
	dir := filepath.Join(stateDir, "knowledge", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("needle line"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := v.knowledgeSearch(context.Background(), "needle")
	if err != nil {
		t.Fatalf("search=%v", err)
	}
	fragments, _ := out["fragments"].([]knowledgeFragment)
	if len(fragments) != 1 || fragments[0].Source != "notes.md" || fragments[0].SHA256 == "" {
		t.Fatalf("fragments=%v", out)
	}
	// Hit cap: more than 8 matching lines still report at most 8 fragments.
	if err := os.WriteFile(filepath.Join(dir, "many.md"), []byte(strings.Repeat("needle\n", 20)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = v.knowledgeSearch(context.Background(), "needle")
	if err != nil {
		t.Fatalf("search=%v", err)
	}
	if got := len(out["fragments"].([]knowledgeFragment)); got > 8 {
		t.Fatalf("hit cap exceeded: %d", got)
	}
	if out["scan_limit"] != 128 {
		t.Fatalf("scan_limit=%v", out["scan_limit"])
	}
	// Scan cap: a corpus wider than the bound is not exhaustively walked.
	for i := 0; i < 140; i++ {
		if err := os.WriteFile(filepath.Join(dir, strings.Repeat("f", 8)+string(rune('a'+i%26))+string(rune('0'+i%10))+".md"), []byte("needle"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err = v.knowledgeSearch(context.Background(), "needle")
	if err != nil {
		t.Fatalf("search=%v", err)
	}
	if scanned, _ := out["scanned"].(int); scanned > 128 {
		t.Fatalf("scan bound exceeded: %v", scanned)
	}
	read, err := v.knowledgeRead(context.Background(), Input{Name: "notes.md"})
	if err != nil || read["sha256"] == "" || read["context_id"] != "alice" {
		t.Fatalf("provenance=%v %v", read, err)
	}
	for _, query := range []string{"", strings.Repeat("x", 257)} {
		if _, err = v.knowledgeSearch(context.Background(), query); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "oversize.md"), []byte(strings.Repeat("x", (9<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "oversize.md"}); err == nil {
		t.Fatal("oversize knowledge silently truncated")
	}
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "..\\..\\escape"}); err == nil {
		t.Fatal("path escape accepted")
	}
	v.StateDir = filepath.Join(t.TempDir(), "missing")
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "notes.md"}); err == nil {
		t.Fatal("unavailable state dir accepted")
	}
}

// A context-specific staging dir narrows the root when the runtime's
// context differs from the principal (per-context staging contract).
func TestKnowledgeContextSubdirNarrowsRoot(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "project")
	v := fixture(t)
	ctxDir := filepath.Join(stateDir, "knowledge", "alice", "project")
	if err := os.MkdirAll(ctxDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "ctx.md"), []byte("context snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Join(stateDir, "knowledge", "alice")
	if err := os.WriteFile(filepath.Join(rootDir, "root.md"), []byte("principal notes"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := v.knowledgeRead(context.Background(), Input{Name: "ctx.md"})
	if err != nil || out["content"] != "context snapshot" {
		t.Fatalf("context read=%v %v", out, err)
	}
	if _, err := v.knowledgeRead(context.Background(), Input{Name: "root.md"}); err == nil {
		t.Fatal("parent dir readable past the context root")
	}
}
