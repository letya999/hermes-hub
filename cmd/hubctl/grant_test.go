package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestGrantWritesOperatorRecordAndRejectsModelIssuer(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	if err := runGrant(context.Background(), []string{"--kind", "self-install", "--user", "alice", "--toolhub-store", storePath, "--dir", dir}); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if err := store.RequireSelfInstall(auth); err != nil {
		t.Fatal(err)
	}
	bad := toolhub.Grant{Schema: toolhub.SchemaVersion, GrantID: "grant-model", Kind: toolhub.GrantSelfInstall, PrincipalID: "alice", IssuedBy: "model", Status: toolhub.ActiveStatus, Revision: 1}
	if err := store.PutGrant(bad); err == nil {
		t.Fatal("model-issued grant accepted")
	}
}

func TestGrantJSONOmitsSecrets(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	runErr := runGrant(context.Background(), []string{"--kind", "catalog-default", "--user", "alice", "--toolhub-store", storePath, "--dir", dir})
	writer.Close()
	os.Stdout = old
	body, _ := io.ReadAll(reader)
	reader.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	stdout := string(body)
	if strings.Contains(strings.ToLower(stdout), "token=") || strings.Contains(stdout, "BEGIN") {
		t.Fatalf("grant output leaked secret-like text: %s", stdout)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil || decoded["kind"] != "catalog-default" || decoded["principal_id"] != "alice" {
		t.Fatalf("grant output=%s err=%v", stdout, err)
	}
}

func TestGrantCLIErrorPathsAndDefinitionKind(t *testing.T) {
	dir := t.TempDir()
	if err := runGrant(context.Background(), []string{"--not-a-flag"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runGrant(context.Background(), []string{"--user", "alice", "--dir", dir}); err == nil || !strings.Contains(err.Error(), "--kind") {
		t.Fatalf("missing kind: %v", err)
	}
	if err := runGrant(context.Background(), []string{"--kind", "nope", "--user", "alice", "--dir", dir, "--toolhub-store", filepath.Join(dir, "bad-kind.json")}); err == nil {
		t.Fatal("invalid kind accepted")
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGrant(context.Background(), []string{"--kind", "self-install", "--user", "alice", "--dir", dir, "--toolhub-store", corrupt}); err == nil {
		t.Fatal("corrupt store accepted")
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGrant(context.Background(), []string{"--kind", "self-install", "--user", "alice", "--dir", dir, "--toolhub-store", filepath.Join(blocked, "store.json")}); err == nil {
		t.Fatal("unwritable store parent accepted")
	}

	absStore := filepath.Join(dir, "absolute.json")
	if err := runGrant(context.Background(), []string{"--kind", "self-install", "--user", "alice", "--toolhub-store", absStore}); err != nil {
		t.Fatalf("default dir: %v", err)
	}

	t.Setenv("HUB_TOOLHUB_STORE", "")
	if err := runGrant(context.Background(), []string{"--kind", "catalog-default", "--user", "bob", "--dir", dir}); err != nil {
		t.Fatalf("default store path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime", "toolhub", "store.json")); err != nil {
		t.Fatal(err)
	}

	relDir := t.TempDir()
	t.Chdir(relDir)
	if err := runGrant(context.Background(), []string{"--kind", "definition", "--definition", "catalog-read", "--version", "1.0.0", "--user", "alice", "--dir", relDir, "--toolhub-store", "relative.json"}); err != nil {
		t.Fatalf("relative store: %v", err)
	}
	store, err := toolhub.Load(filepath.Join(relDir, "relative.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if err := store.RequireCatalogAccess(auth, toolhub.ToolDefinition{DefinitionID: "catalog-read", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	runErr := run(context.Background(), []string{"grant", "--kind", "self-install", "--user", "carol", "--dir", dir, "--toolhub-store", filepath.Join(dir, "via-run.json")})
	writer.Close()
	os.Stdout = old
	body, _ := io.ReadAll(reader)
	reader.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	var decoded map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil || decoded["principal_id"] != "carol" || decoded["kind"] != "self-install" {
		t.Fatalf("run grant output=%s err=%v", body, err)
	}
}
