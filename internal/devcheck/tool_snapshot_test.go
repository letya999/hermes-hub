package devcheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolCatalogSnapshotUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/tool-catalog.snapshot")
	if err != nil {
		t.Fatal(err)
	}
	got, err := toolCatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("tool name/schema/effect/right changed: review the contract, bump hub-agent-tools, then regenerate the snapshot")
	}
	// PR checkouts include the target parent. Source archives have no Git
	// history; they still run the exact catalog/snapshot equality above.
	probe := exec.Command("git", "rev-parse", "--git-dir")
	probe.Dir = "../.."
	if probe.Run() != nil {
		return // source archive without Git metadata
	}
	// The baseline is the fork point with the integration branch, not HEAD^:
	// splitting the schema change and the snapshot regen across two commits
	// leaves HEAD^ already drifted, while the merge-base predates both.
	refs := snapshotBaselineRefs(t)
	checked := false
	for _, ref := range refs {
		cmd := exec.Command("git", "show", ref+":"+toolSnapshotPath)
		cmd.Dir = "../.."
		previous, err := cmd.Output()
		if err != nil {
			continue
		}
		checked = true
		if !bytes.Equal(previous, want) && snapshotVersion(previous) == snapshotVersion(want) {
			t.Fatal("snapshot differs from the integration baseline without a catalog version bump")
		}
		break
	}
	if !checked {
		t.Fatal("git history present but no snapshot baseline at merge-base, origin refs or HEAD^")
	}
}

// snapshotBaselineRefs orders baseline candidates from most to least
// authoritative: merge-base with the integration branches (predates every
// commit in the change), the branch tips, then HEAD^ for history without
// remote refs.
func snapshotBaselineRefs(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var refs []string
	add := func(ref string) {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	for _, branch := range []string{"origin/dev", "origin/main"} {
		cmd := exec.Command("git", "merge-base", "HEAD", branch)
		cmd.Dir = "../.."
		if out, err := cmd.Output(); err == nil {
			add(strings.TrimSpace(string(out)))
		}
	}
	add("origin/dev")
	add("origin/main")
	add("HEAD^")
	return refs
}

func TestToolCatalogSnapshotRequiresVersionBump(t *testing.T) {
	root := t.TempDir()
	if err := WriteToolCatalogSnapshot(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(toolSnapshotPath))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte("# name schema"), []byte("# changed schema"), 1)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteToolCatalogSnapshot(root); err == nil {
		t.Fatal("overwrote drift at the same catalog version")
	}
}
