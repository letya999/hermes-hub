package devcheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	checked := false
	for _, ref := range []string{"origin/dev", "HEAD^"} {
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
		t.Fatal("git history present but no snapshot baseline at origin/dev or HEAD^")
	}
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
