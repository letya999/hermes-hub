package agenttools

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestExecutorNeverOpensToolHubStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store.json")
	original := []byte("not a ToolHub store\n")
	if err := os.WriteFile(store, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", store)
	t.Setenv("HUB_STATE", t.TempDir())
	tools, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("executor consulted another module's store: %v", err)
	}
	defer tools.Close()
	tools.ToolPolicy = &api.ToolPolicySnapshot{}
	if _, err := tools.ServiceEnable(Input{Service: "gitlab"}); err == nil {
		t.Fatal("legacy service enable claimed success")
	}
	if _, err := tools.ServiceDisable(Input{Service: "demo"}); err == nil {
		t.Fatal("legacy service disable claimed success")
	}
	got, err := os.ReadFile(store)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("executor changed another module's store")
	}
}
