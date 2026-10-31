package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/memorytest"
)

// The vault is a memory.Provider: the shared conformance suite — scope
// isolation, idempotent capture, provenance round-trip and hash-checked
// writes — runs against it unchanged (ADR-0086 D15).
func TestConformanceVault(t *testing.T) {
	memorytest.Conformance(t, func(t *testing.T) memorytest.Provider {
		root := filepath.Join(t.TempDir(), "vault")
		if err := Ensure(root); err != nil {
			t.Fatal(err)
		}
		return NewProvider(root)
	})
}

// Recall and Search arrive in M20.3/M20.4 — until then the provider reports
// them honestly as unsupported rather than faking empty results (ADR-0086
// D15). Health is the only lane that answers today.
func TestProviderHonestUnsupported(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	p := NewProvider(root)
	scope := memory.MintScope([]byte("m"), "prod", "alice", "private")
	if _, err := p.Recall(context.Background(), scope); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Recall must report ErrUnsupported, got %v", err)
	}
	if _, err := p.Search(context.Background(), scope, memory.Query{}); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Search must report ErrUnsupported, got %v", err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

// A corrupt scope journal surfaces as an error — capture and export never
// silently absorb or drop entries (SPEC-0085 R2.7 replay relies on this).
func TestCaptureCorruptJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	p := NewProvider(root)
	scope := memory.MintScope([]byte("m"), "prod", "alice", "private")
	journal := filepath.Join(root, "runtime", "memory", "entries", scope.Key+".jsonl")
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{not json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{ID: "e1", Scope: scope, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate}
	if _, err := p.Capture(context.Background(), entry); err == nil {
		t.Fatal("capture over a corrupt journal must fail")
	}
	if _, err := p.Export(context.Background(), scope); err == nil {
		t.Fatal("export over a corrupt journal must fail")
	}
}
