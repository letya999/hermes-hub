package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/memorytest"
	"github.com/letya999/hermes-hub/internal/vault"
)

// R11.4: `memory replay` streams the prov.db audit spine into the rendered
// provider — minting opaque bank ids from hub-held material — and a re-run
// captures nothing twice.
func TestMemoryReplayCLI(t *testing.T) {
	fx, srv := memorytest.NewHindsightFixture(t)
	dir := t.TempDir()
	writeManagedSpaceDocs(t, dir, "alice", nil)
	vaultDir := filepath.Join(dir, "vault")
	if err := vault.Ensure(vaultDir); err != nil {
		t.Fatal(err)
	}
	prov, err := memory.OpenProvDB(filepath.Join(vaultDir, "runtime", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []memory.Entry{
		{Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
			Audience: memory.AudiencePrivate, Body: "first fact", ObservedAt: time.Now().UTC()},
		{Kind: memory.KindInteractive, Provenance: memory.ProvenanceAgent,
			Audience: memory.AudienceGroup, Body: "group fact", ObservedAt: time.Now().UTC()},
	} {
		if err := prov.Append(&e); err != nil {
			t.Fatal(err)
		}
	}
	memDir := filepath.Join(vaultDir, "runtime", "memory")
	cfg, _ := json.Marshal(map[string]string{"provider": "hindsight", "url": srv.URL})
	if err := os.WriteFile(filepath.Join(memDir, "provider.json"), append(cfg, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.prod.env"), []byte("# none\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runMemory(context.Background(), []string{"replay", "-dir", dir, "-env", "prod", "-user", "alice"}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	material, err := vault.OpenScopeMaterial(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	priv := memory.MintScope(material, "prod", "alice", string(memory.AudiencePrivate))
	grp := memory.MintScope(material, "prod", "alice", string(memory.AudienceGroup))
	if len(fx.Docs(priv.Key)) != 1 {
		t.Fatalf("private bank holds %d docs, want 1", len(fx.Docs(priv.Key)))
	}
	if len(fx.Docs(grp.Key)) != 1 {
		t.Fatalf("group bank holds %d docs, want 1", len(fx.Docs(grp.Key)))
	}
	if _, err := os.Stat(filepath.Join(memDir, "replay.cursor")); err != nil {
		t.Fatalf("cursor missing: %v", err)
	}
	// Re-run: idempotent — still exactly one doc per bank.
	if err := runMemory(context.Background(), []string{"replay", "-dir", dir, "-env", "prod", "-user", "alice"}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(fx.Docs(priv.Key)) != 1 || len(fx.Docs(grp.Key)) != 1 {
		t.Fatal("re-run duplicated entries")
	}
	// No rendered provider config → honest failure, not a silent vault noop.
	bare := t.TempDir()
	if err := vault.Ensure(filepath.Join(bare, "vault")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "settings.yaml"),
		[]byte("schema: 1\nuser: alice\nmodel: test\noauth_port: 8000\nbrowser_port: 6080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runMemory(context.Background(), []string{"replay", "-dir", bare, "-env", "prod"}); err == nil {
		t.Fatal("replay without a rendered provider must fail")
	}
}

// R11.5: `memory export-honcho --file` stages reviewable cards; only
// --apply commits them into notes/inbox — and a second apply cannot
// clobber or duplicate.
func TestMemoryExportHonchoCLI(t *testing.T) {
	dir := t.TempDir()
	if err := vault.Ensure(filepath.Join(dir, "vault")); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(dir, "honcho.json")
	if err := os.WriteFile(export, []byte(`{"items":[
	  {"id":"c1","content":"prefers earl grey","observer_id":"a","observed_id":"alice","level":"explicit","times_derived":1,"created_at":"2026-10-01T10:00:00Z"}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "vault", "runtime", "memory", "import", "honcho-alice.md")
	card := filepath.Join(dir, "vault", "memory", "cards", "notes", "inbox", "honcho-alice.md")

	if err := runMemory(context.Background(), []string{"export-honcho", "-dir", dir, "-file", export}); err != nil {
		t.Fatalf("export-honcho: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("draft not staged: %v", err)
	}
	if _, err := os.Stat(card); !os.IsNotExist(err) {
		t.Fatal("export-honcho landed a card without --apply")
	}
	if err := runMemory(context.Background(), []string{"export-honcho", "-dir", dir, "-file", export, "-apply"}); err != nil {
		t.Fatalf("export-honcho --apply: %v", err)
	}
	body, err := os.ReadFile(card)
	if err != nil {
		t.Fatalf("card missing after --apply: %v", err)
	}
	if !strings.Contains(string(body), "source: migrated") {
		t.Fatalf("card missing source:migrated: %s", body)
	}
	// Missing --file fails closed.
	if err := runMemory(context.Background(), []string{"export-honcho", "-dir", dir}); err == nil {
		t.Fatal("export-honcho without --file must fail")
	}
}
