package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// R12.5: `hubctl skill stats` surfaces the per-skill counters the hub
// journaled — counters only, never event detail.
func TestSkillStatsReportsCounters(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	st, err := skillstore.Open(store, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	md := "---\nname: demo\ndescription: Use when testing.\n---\nbody\n"
	if _, err := st.Publish("demo", skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: []byte(md)}}, skillstore.IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := skillstore.RecordUsage(store, "demo", "use", ""); err != nil {
		t.Fatal(err)
	}
	if err := skillstore.RecordUsage(store, "demo", "failed", "private note"); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return runSkill(context.Background(), []string{"stats", "--dir", dir, "--name", "demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var stats skillstore.UsageStats
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("stats output %q: %v", out, err)
	}
	if stats.Uses != 1 || stats.Failed != 1 || stats.LastUsed == "" {
		t.Fatalf("stats %+v", stats)
	}
	if strings.Contains(out, "private note") {
		t.Fatal("event detail must not reach the stats surface")
	}
}
