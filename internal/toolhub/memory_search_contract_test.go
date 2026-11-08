package toolhub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// searchVaultFixture materializes a real enrolled vault with one private
// leaf — the #515 handler's proof needs a hit the indexer can evidence.
func searchVaultFixture(t *testing.T, vaults, passports, user, environment string) {
	t.Helper()
	writeMemoryPassportEnv(t, passports, user, environment, true)
	dir := filepath.Join(vaults, user)
	if err := vault.Ensure(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	daily, err := vault.OpenDaily(dir, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.Entry{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "we picked the earl grey supplier", ObservedAt: time.Now()}
	if err := daily.Append(entry, "telegram"); err != nil {
		t.Fatal(err)
	}
}

// R3.3/R3.5 on the #515 path: the ToolHub-owned memory_search returns
// evidence-linked private hits with the same contract the in-runtime
// handler served — file:line refs, honest status, untrusted flag.
func TestSearchToolEvidenceRefs(t *testing.T) {
	vaults, passports := t.TempDir(), t.TempDir()
	searchVaultFixture(t, vaults, passports, "alice", "dev")
	resolver, err := NewVaultResolver(vaults, passports)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Vaults: resolver}
	auth := identity.Envelope{PrincipalID: "alice", Environment: "dev"}
	if _, err := g.memorySearch(t.Context(), auth, map[string]any{"query": "earl grey"}); err != nil {
		t.Fatalf("warm: %v", err)
	}
	out, err := g.memorySearch(t.Context(), auth, map[string]any{"query": "earl grey"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hits, _ := out["hits"].([]map[string]any)
	if len(hits) != 1 {
		t.Fatalf("hits %v want 1", out["hits"])
	}
	if ev, _ := hits[0]["evidence"].(string); ev == "" {
		t.Fatalf("hit missing evidence: %v", hits[0])
	}
	if _, present := hits[0]["untrusted"]; !present {
		t.Fatalf("hit missing untrusted flag: %v", hits[0])
	}
	if out["status"] != "ok" {
		t.Fatalf("status %v", out["status"])
	}
}

func TestReadToolExactOwnSearchHit(t *testing.T) {
	vaults, passports := t.TempDir(), t.TempDir()
	searchVaultFixture(t, vaults, passports, "alice", "dev")
	writeMemoryPassportEnv(t, passports, "bob", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "bob")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewVaultResolver(vaults, passports)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Vaults: resolver}
	auth := identity.Envelope{PrincipalID: "alice", Environment: "dev"}
	searched, err := g.memorySearch(t.Context(), auth, map[string]any{"query": "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	hits, ok := searched["hits"].([]map[string]any)
	if !ok || len(hits) != 1 {
		t.Fatalf("search hits=%d", len(hits))
	}
	id, _ := hits[0]["id"].(string)
	read, err := g.memoryRead(t.Context(), auth, map[string]any{"id": id})
	if err != nil || read["status"] == "unavailable" || read["status"] == "not_found" {
		t.Fatalf("read status=%v err=%v", read["status"], err)
	}
	hit, ok := read["hit"].(map[string]any)
	if !ok || hit["text"] != "we picked the earl grey supplier" || hit["evidence"] != hits[0]["evidence"] {
		t.Fatalf("read did not match exact search hit")
	}
	if denied, err := g.memoryRead(t.Context(), identity.Envelope{PrincipalID: "bob", Environment: "dev"}, map[string]any{"id": id}); err != nil || denied["status"] != "not_found" {
		t.Fatalf("foreign ID result=%v err=%v", denied["status"], err)
	}
}
