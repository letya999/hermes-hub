package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWorkloadCredentialFileCannotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "CLIENT_FILE.secret")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := WriteWorkloadCredentialFile(root, "CLIENT_FILE", "synthetic-secret")
	body, readErr := os.ReadFile(outside)
	if readErr != nil || string(body) != "unchanged" {
		t.Error("credential write escaped workload directory")
	}
	if err == nil {
		info, err := os.Lstat(link)
		if err != nil || !info.Mode().IsRegular() {
			t.Error("successful credential write retained a symlink")
		}
	}
}
