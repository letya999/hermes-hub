package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
)

func TestDecryptAuthorizedFiltersAndSharedStatic(t *testing.T) {
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "store.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	locator := "local://alice/google/1"
	if err := backend.Put(locator, "alice", map[string]string{"GOOGLE_TOKEN": "keep-me", "OTHER_TOKEN": "drop-me"}); err != nil {
		t.Fatal(err)
	}
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecryptAuthorized(backend, effective)
	if err != nil || env["GOOGLE_TOKEN"] != "keep-me" || env["OTHER_TOKEN"] != "" {
		t.Fatalf("decrypt=%v err=%v", env, err)
	}
	shared := effective
	shared.Definition.Workload.Class = Shared
	shared.Definition.Credentials[0].PerRequest = false
	if _, err := DecryptAuthorized(backend, shared); err == nil {
		t.Fatal("shared static accepted")
	}
	empty, err := DecryptAuthorized(backend, EffectiveBinding{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing credential decrypt=%v err=%v", empty, err)
	}
	refs := store.ConnectionsForSecret(locator, "alice", []string{"GOOGLE_TOKEN"})
	if len(refs) != 1 || refs[0].ConnectionID != "google-work" {
		t.Fatalf("connections=%+v", refs)
	}
	if refs := store.ConnectionsForSecret("missing", "bob", nil); len(refs) != 0 {
		t.Fatalf("cross-user connections=%+v", refs)
	}
}

func TestWriteAuthorizedFilesAndCorrelation(t *testing.T) {
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wipe, err := writeAuthorizedFiles(context.Background(), root, effective, map[string]string{"GOOGLE_TOKEN": "file-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wipe() })
	workspace, err := OpenWorkloadWorkspace(root, effective, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(workspace.Path, "credentials.env"))
	if err != nil || !strings.Contains(string(body), "file-secret") {
		t.Fatalf("file=%s err=%v", body, err)
	}
	if err := wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Path, "credentials.env")); !os.IsNotExist(err) {
		t.Fatal("credentials.env survived wipe")
	}
	if _, err := writeAuthorizedFiles(context.Background(), "", effective, map[string]string{"GOOGLE_TOKEN": "x"}); err == nil {
		t.Fatal("empty root accepted")
	}
	corr := headerCorrelation("job-alice", "run-alice")
	if corr.JobID != "job-alice" || corr.HermesRunID != "run-alice" {
		t.Fatalf("corr=%+v", corr)
	}
	if headerCorrelation("bad\nid", "run-alice").JobID != "" {
		t.Fatal("newline job id accepted")
	}
	if id := newToolCallID(); !strings.HasPrefix(id, "call-") {
		t.Fatalf("tool call id=%q", id)
	}
}
