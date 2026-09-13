package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresStore(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	if err := run(); err == nil {
		t.Fatal("missing ToolHub store accepted")
	}
}

func TestRunRejectsInvalidListenAddressAfterLoadingStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "invalid-listen-address")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	if err := run(); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}
