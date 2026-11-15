package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
)

func TestSecretActivityShowsOperationWithoutCredentialKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "owner")
	if err := run(context.Background(), []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "runtime", "credentials", "store.enc")
	ledger, err := audit.Open(filepath.Join(filepath.Dir(store), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := audit.NewEvent("credential-change", "owner", "set")
	legacy.Name = "FIXTUREBEARERDICTIONARYKEY"
	if err := ledger.Append(legacy); err != nil {
		t.Fatal(err)
	}
	event := audit.NewEvent("credential-change", "owner", "admitted")
	event.Action, event.ContextID = "credential.set", "owner"
	if _, err := ledger.Begin(event); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(context.Background(), []string{"secret", "activity", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(out, legacy.Name) {
		t.Fatal("activity disclosed a credential dictionary key")
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row["phase"] == "admitted" && row["operation_id"] != "" && row["authorization"] == "allow" && row["execution"] == "unknown" {
			return
		}
	}
	t.Fatal("uncertain operation identity/outcome omitted from activity")
}
