// Copyright (c) Cognition Labs and/or its affiliates.
// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// SPEC-0085 R7.5: `hubctl memory prune` dry-runs by default — the leaf
// stays; --apply expires a retention-age day once its gates pass.
func TestMemoryPruneCLI(t *testing.T) {
	dir := t.TempDir()
	writeManagedSpaceDocs(t, dir, "alice", map[string]string{
		"config": "kind: config\nruntime: {timezone: UTC}\nmemory: {enabled: true, leaf_retention_days: 7}\n",
		"tools":  "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  files: {via: toolhub}\nextended:\n  memory_vault: {via: native}\n",
	})
	if err := os.WriteFile(filepath.Join(dir, "runtime.prod.env"), []byte("# none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vaultDir := filepath.Join(dir, "vault")
	if err := vault.Ensure(vaultDir); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -30)
	daily, err := vault.OpenDaily(vaultDir, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.Append(memory.Entry{ID: "old-1", Kind: memory.KindInteractive,
		Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate,
		Body: "old content", ObservedAt: old}, "test"); err != nil {
		t.Fatal(err)
	}
	dayKey := old.Format("2006-01-02")
	cache := filepath.Join(vaultDir, "runtime", "memory", "night", dayKey+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := vault.NewWriter(vaultDir)
	if _, err := w.Apply(memory.WriteOp{Path: "memory/summaries/daily/" + dayKey + ".md",
		Content: []byte("---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\nold digest\n"),
		Actor:   memory.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(vaultDir, "memory", "daily", dayKey+".md")

	if err := runMemory(context.Background(), []string{"prune", "-dir", dir, "-env", "prod"}); err != nil {
		t.Fatalf("prune dry: %v", err)
	}
	if _, err := os.Stat(leaf); err != nil {
		t.Fatalf("dry-run must not expire the leaf: %v", err)
	}
	if err := runMemory(context.Background(), []string{"prune", "-dir", dir, "-env", "prod", "-apply"}); err != nil {
		t.Fatalf("prune apply: %v", err)
	}
	if _, err := os.Stat(leaf); !os.IsNotExist(err) {
		t.Fatal("expired leaf still present after --apply")
	}
}
