package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
)

func TestCLIWorkflow(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "owner")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"doctor", "--dir", dir}); err == nil {
		t.Fatal("doctor accepted missing model")
	}
	if err := run(ctx, []string{"render", "--dir", dir, "--root", "../.."}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.prod.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"unknown"}, {"init", "--dir", dir}, {"render", "unexpected"}, {"init", "--user", "../other"}} {
		if err := run(ctx, args); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
}

func TestCLIOrganizationWorkflow(t *testing.T) {
	root := t.TempDir()
	org := filepath.Join(root, "organizations", "acme")
	space := filepath.Join(root, "spaces", "alice")
	if err := run(context.Background(), []string{"org-init", "--org", "acme", "--org-dir", org, "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"init", "--dir", space, "--user", "alice", "--org", "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"doctor", "--dir", space}); err == nil {
		t.Fatal("organization doctor accepted missing model")
	}
	if err := run(context.Background(), []string{"org-init", "--org-dir", org}); err == nil {
		t.Fatal("org-init without organization accepted")
	}
}

func TestCLISecretSetListDelete(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "owner")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "credential.key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	valueFile := filepath.Join(t.TempDir(), "value.txt")
	secret := "cli-secret-" + hex.EncodeToString(key[:8])
	if err := os.WriteFile(valueFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "runtime", "credentials", "store.enc")
	for i := 0; i < 2; i++ {
		out := captureOutput(t, func() error {
			return run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--name", "GOOGLE_TOKEN", "--from-file", valueFile, "--key-file", keyFile, "--store", store})
		})
		if strings.Contains(out, secret) || !strings.Contains(out, "GOOGLE_TOKEN") || !strings.Contains(out, "active") {
			t.Fatalf("set output=%q", out)
		}
		listed := captureOutput(t, func() error {
			return run(ctx, []string{"secret", "list", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store})
		})
		if strings.Contains(listed, secret) || !strings.Contains(listed, "GOOGLE_TOKEN") {
			t.Fatalf("list output=%q", listed)
		}
	}
	body, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("ciphertext store contained plaintext")
	}
	deleted := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "delete", "--dir", dir, "--user", "owner", "--name", "GOOGLE_TOKEN", "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(deleted, secret) || !strings.Contains(deleted, "removed") {
		t.Fatalf("delete output=%q", deleted)
	}
	listed := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "list", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(listed, "GOOGLE_TOKEN") {
		t.Fatalf("list after delete=%q", listed)
	}
	assign := filepath.Join(t.TempDir(), "assign.txt")
	if err := os.WriteFile(assign, []byte("SLACK_TOKEN="+secret), 0600); err != nil {
		t.Fatal(err)
	}
	set2 := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--from-file", assign, "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(set2, secret) || !strings.Contains(set2, "SLACK_TOKEN") {
		t.Fatalf("assignment set=%q", set2)
	}
	expose := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "expose", "--dir", dir, "--user", "owner", "--name", "SLACK_TOKEN", "--terminal", "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(expose, secret) || !strings.Contains(expose, "terminal") {
		t.Fatalf("expose=%q", expose)
	}
	activity := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "activity", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store})
	})
	if strings.Contains(activity, secret) || !strings.Contains(activity, "credential-change") {
		t.Fatalf("activity=%q", activity)
	}
	backup := filepath.Join(t.TempDir(), "cred.backup")
	if err := run(ctx, []string{"secret", "backup", "--dir", dir, "--user", "owner", "--out", backup, "--key-file", keyFile, "--store", store}); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(backup)
	if err != nil || strings.Contains(string(body), secret) {
		t.Fatalf("backup leaked plaintext: %v", err)
	}
	if err := run(ctx, []string{"secret", "restore", "--dir", dir, "--user", "owner", "--in", backup, "--key-file", keyFile, "--store", store}); err != nil {
		t.Fatal(err)
	}
	newKey, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newKeyFile := filepath.Join(t.TempDir(), "new.key")
	if err := credstore.WriteKeyFile(newKeyFile, newKey); err != nil {
		t.Fatal(err)
	}
	rotated := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "rotate-key", "--dir", dir, "--user", "owner", "--from-file", newKeyFile, "--key-file", keyFile, "--store", store})
	})
	if !strings.Contains(rotated, "key-rotated") {
		t.Fatalf("rotate-key=%q", rotated)
	}
	if _, err := parseAssignments("SLACK_TOKEN=x"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAssignments("not-an-assignment"); err == nil {
		t.Fatal("invalid assignment accepted")
	}
	if _, err := decodeHexKey("zz"); err == nil {
		t.Fatal("short key accepted")
	}
	if got := defaultCredentialKeyPath(); got == "" {
		t.Fatal("empty default key path")
	}
	if err := run(ctx, []string{"secret"}); err == nil {
		t.Fatal("secret without subcommand accepted")
	}
	if err := run(ctx, []string{"secret", "nope", "--key-file", keyFile, "--store", store, "--dir", dir, "--user", "owner"}); err == nil {
		t.Fatal("unknown secret command accepted")
	}
	generatedKey := filepath.Join(t.TempDir(), "generated.key")
	store2 := filepath.Join(dir, "runtime", "credentials", "store2.enc")
	stdinFile := filepath.Join(t.TempDir(), "stdin-value")
	if err := os.WriteFile(stdinFile, []byte("from-stdin-"+secret), 0600); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	in, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = in
	stdinOut := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--name", "STDIN_TOKEN", "--key-file", generatedKey, "--store", store2})
	})
	os.Stdin = oldStdin
	_ = in.Close()
	if strings.Contains(stdinOut, secret) || !strings.Contains(stdinOut, "STDIN_TOKEN") {
		t.Fatalf("stdin set=%q", stdinOut)
	}
	t.Setenv("APPDATA", "")
	if got := defaultCredentialKeyPath(); !strings.Contains(got, "hermes-hub") {
		t.Fatalf("default key path=%q", got)
	}
}

func TestCLISecretErrorPaths(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "owner")
	if err := run(ctx, []string{"init", "--dir", dir, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "credential.key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "runtime", "credentials", "store.enc")
	valueFile := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(valueFile, []byte("TOKEN=cli-secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--from-file", valueFile, "--key-file", keyFile, "--store", store}); err != nil {
		t.Fatal(err)
	}
	isolated := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "expose", "--dir", dir, "--user", "owner", "--name", "TOKEN", "--key-file", keyFile, "--store", store})
	})
	if !strings.Contains(isolated, "isolated") {
		t.Fatalf("expose isolated=%q", isolated)
	}
	if err := run(ctx, []string{"secret", "delete", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("delete without name accepted")
	}
	if err := run(ctx, []string{"secret", "expose", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("expose without name accepted")
	}
	if err := run(ctx, []string{"secret", "backup", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("backup without out accepted")
	}
	if err := run(ctx, []string{"secret", "restore", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("restore without in accepted")
	}
	if err := run(ctx, []string{"secret", "rotate-key", "--dir", dir, "--user", "owner", "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("rotate-key without from-file accepted")
	}
	badKey := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(badKey, []byte("not-hex"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "rotate-key", "--dir", dir, "--user", "owner", "--from-file", badKey, "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("rotate-key bad hex accepted")
	}
	shortKey := filepath.Join(t.TempDir(), "short.key")
	if err := os.WriteFile(shortKey, []byte("abcd"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "rotate-key", "--dir", dir, "--user", "owner", "--from-file", shortKey, "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("rotate-key short key accepted")
	}
	huge := filepath.Join(t.TempDir(), "huge.txt")
	if err := os.WriteFile(huge, make([]byte, credstore.MaxValueBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--name", "HUGE", "--from-file", huge, "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("oversized secret accepted")
	}
	emptyAssign := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyAssign, []byte("\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"secret", "set", "--dir", dir, "--user", "owner", "--from-file", emptyAssign, "--key-file", keyFile, "--store", store}); err == nil {
		t.Fatal("empty assignment accepted")
	}
	if err := run(ctx, []string{"secret", "set", "--bogus"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", "")
	t.Setenv("HUB_CREDENTIAL_STORE", "")
	fresh := filepath.Join(t.TempDir(), "fresh")
	if err := run(ctx, []string{"init", "--dir", fresh, "--user", "owner"}); err != nil {
		t.Fatal(err)
	}
	listed := captureOutput(t, func() error {
		return run(ctx, []string{"secret", "list", "--dir", fresh, "--user", "owner"})
	})
	if strings.Contains(listed, "cli-secret-value") {
		t.Fatalf("default store list leaked value: %q", listed)
	}
	if _, err := parseAssignments("\nTOKEN=x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeHexKey(hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialKey(keyFile); err != nil {
		t.Fatal(err)
	}
	missingFile := filepath.Join(t.TempDir(), "missing.txt")
	if _, err := readSecretInput(missingFile); err == nil {
		t.Fatal("missing from-file accepted")
	}
}

func captureOutput(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	return buf.String()
}

func TestCLISupervisorLifecycle(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	if err := run(context.Background(), []string{"supervisor", "--spaces", t.TempDir()}); err == nil {
		t.Fatal("supervisor started without control auth")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"supervisor", "--spaces", root, "--supervisor-auth", "secret", "--supervisor-listen", "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
}
