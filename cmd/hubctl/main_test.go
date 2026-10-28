package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"gopkg.in/yaml.v3"
)

// hubctl up shares the supervisor's warm-retention default: bare flags must
// keep 30 minutes, not the old five.
func TestWarmTTLFlagDefault(t *testing.T) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	ttl := warmTTLFlag(fs)
	if *ttl != supervisor.DefaultWarmTTL {
		t.Fatalf("warm-ttl default=%s, want %s", *ttl, supervisor.DefaultWarmTTL)
	}
}

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

// `hubctl init --profile` stamps the chosen provisioning profile's files.
func TestCLIInitProfile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	profileDir := filepath.Join(root, "policies", "profiles", "dev")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "workspace.yaml"), []byte("tools: {}\n# dev-wishes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	space := filepath.Join(root, "spaces", "carol")
	if err := run(ctx, []string{"init", "--dir", space, "--user", "carol", "--profile", "dev"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if err != nil || string(workspace) != "tools: {}\n# dev-wishes\n" {
		t.Fatalf("profile workspace not stamped: %q %v", workspace, err)
	}
}

// `hubctl org sync-members` provisions every member space idempotently with
// the member's resolved profile; dry-run reports without writing.
func TestCLIOrgSyncMembers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	orgDir := filepath.Join(root, "organizations", "acme")
	if err := run(ctx, []string{"org-init", "--org", "acme", "--org-dir", orgDir, "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := stack.InitOrganization(orgDir, "acme", "alice"); err == nil {
		t.Fatal("org-init overwrote")
	}
	orgSettings, err := stack.ReadOrganization(orgDir)
	if err != nil {
		t.Fatal(err)
	}
	orgSettings.Members["bob"] = stack.OrgMember{Role: "member", Profile: "lead"}
	if err := os.MkdirAll(filepath.Join(orgDir, "profiles", "lead"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgDir, "profiles", "lead", "workspace.yaml"), []byte("tools: {}\n# lead\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(orgSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgDir, "settings.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	spacesRoot := filepath.Join(root, "spaces")

	// Dry-run reports both members without creating anything.
	out, err := captureStdout(t, func() error {
		return run(ctx, []string{"org", "sync-members", "--org", "acme", "--org-dir", orgDir, "--spaces", spacesRoot, "--dry-run"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("dry-run report: %q", out)
	}
	if _, err := os.Stat(filepath.Join(spacesRoot, "bob")); !os.IsNotExist(err) {
		t.Fatal("dry-run created a space")
	}

	if err := run(ctx, []string{"org", "sync-members", "--org", "acme", "--org-dir", orgDir, "--spaces", spacesRoot}); err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(spacesRoot, "bob", "workspace.yaml"))
	if string(workspace) != "tools: {}\n# lead\n" {
		t.Fatalf("member profile not stamped: %q", workspace)
	}
	// Second run is idempotent: reports existing, creates nothing.
	out, err = captureStdout(t, func() error {
		return run(ctx, []string{"org", "sync-members", "--org", "acme", "--org-dir", orgDir, "--spaces", spacesRoot})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"created":[]`) {
		t.Fatalf("second run not idempotent: %q", out)
	}
}

// `hubctl profile grants` renders a profile's grants.yaml into a policy draft
// revision (defaults swapped, ceiling untouched); only the confirmed operator
// capability path can commit it, and the store still rejects anything past
// the organization ceiling (R9.5).
func TestProfileGrantsIssuedByOperatorOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "alice")
	digest := "sha256:" + strings.Repeat("a", 64)
	profileDir := filepath.Join(root, "policies", "profiles", "ops")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	grants := "rules:\n  - capability_id: files\n    implementation_digest: " + digest +
		"\n    action: read\n    resource: workspace\n    path_prefix: docs\n    limits: {output_bytes: 1024, timeout_seconds: 5}\n"
	if err := os.WriteFile(filepath.Join(profileDir, "grants.yaml"), []byte(grants), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"init", "--dir", space, "--user", "alice", "--profile", "ops"}); err != nil {
		t.Fatal(err)
	}

	rule := map[string]any{"capability_id": "files", "implementation_digest": digest,
		"action": "read", "resource": "workspace", "path_prefix": "docs",
		"limits": map[string]any{"output_bytes": 1024, "timeout_seconds": 5}}
	policy := map[string]any{
		"schema": 1, "policy_id": "acme-ops", "organization": "acme", "members": []string{"alice"},
		"revision": 1, "issued_by": "operator", "issued_at": "2026-01-01T00:00:00Z",
		"reason": "acme ops baseline", "status": "active", "ceiling": []any{rule},
	}
	policyBody, _ := json.Marshal(policy)
	policyPath := filepath.Join(root, "policy.json")
	if err := os.WriteFile(policyPath, policyBody, 0600); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(root, "draft.json")
	if err := run(ctx, []string{"profile", "grants", "--dir", space, "--profile", "ops", "--policy", policyPath, "--out", draftPath}); err != nil {
		t.Fatal(err)
	}
	var draft map[string]any
	draftBody, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(draftBody, &draft); err != nil {
		t.Fatal(err)
	}
	defaults, _ := draft["defaults"].([]any)
	if len(defaults) != 1 || defaults[0].(map[string]any)["path_prefix"] != "docs" {
		t.Fatalf("grants did not land in policy defaults: %s", draftBody)
	}
	if draft["revision"] != float64(2) {
		t.Fatalf("draft revision not bumped: %v", draft["revision"])
	}

	// The draft has no authority until an operator confirms it; the capability
	// path then commits it, and the ceiling check still applies.
	store := filepath.Join(root, "toolhub-store.json")
	if err := run(ctx, []string{"capability", "--kind", "policy", "--file", draftPath, "--toolhub-store", store}); err == nil {
		t.Fatal("unconfirmed policy accepted")
	}
	if err := run(ctx, []string{"capability", "--kind", "policy", "--file", draftPath, "--toolhub-store", store, "--confirm"}); err != nil {
		t.Fatalf("confirmed in-ceiling policy rejected: %v", err)
	}
}

// `hubctl profile diff|apply` performs 3-way updates via profile.lock: files
// the user never touched take the new profile content; customized files are
// preserved and reported (R9.7).
func TestProfileApplyKeepsCustomizedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	profileDir := filepath.Join(root, "policies", "profiles", "ops")
	for name, body := range map[string]string{
		"profile.yaml":   "version: 1\n",
		"workspace.yaml": "tools: {}\n# ops v1\n",
		"agent.yaml":     "model: base\n# ops v1\n",
	} {
		writeProfileFixture(t, profileDir, name, body)
	}
	space := filepath.Join(root, "spaces", "alice")
	if err := run(ctx, []string{"init", "--dir", space, "--user", "alice", "--profile", "ops"}); err != nil {
		t.Fatal(err)
	}

	// The owner edits agent.yaml; the profile then moves workspace to v2.
	writeProfileFixture(t, space, "agent.yaml", "model: custom\n# alice edits\n")
	writeProfileFixture(t, profileDir, "workspace.yaml", "tools: {}\n# ops v2\n")
	writeProfileFixture(t, profileDir, "agent.yaml", "model: base\n# ops v2\n")
	writeProfileFixture(t, profileDir, "profile.yaml", "version: 2\n")

	out, err := captureStdout(t, func() error {
		return run(ctx, []string{"profile", "diff", "--dir", space})
	})
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("diff report: %v %q", err, out)
	}
	if !slices.Contains(report["updates"].([]any), "workspace.yaml") ||
		!slices.Contains(report["customized"].([]any), "agent.yaml") {
		t.Fatalf("3-way report wrong: %s", out)
	}

	if err := run(ctx, []string{"profile", "apply", "--dir", space}); err != nil {
		t.Fatal(err)
	}
	workspace, _ := os.ReadFile(filepath.Join(space, "workspace.yaml"))
	if string(workspace) != "tools: {}\n# ops v2\n" {
		t.Fatalf("untouched file not updated: %q", workspace)
	}
	agent, _ := os.ReadFile(filepath.Join(space, "agent.yaml"))
	if string(agent) != "model: custom\n# alice edits\n" {
		t.Fatalf("customized file overwritten: %q", agent)
	}
	lock, _ := os.ReadFile(filepath.Join(space, "profile.lock"))
	if !strings.Contains(string(lock), "version: 2") {
		t.Fatalf("lock not advanced: %q", lock)
	}
}

func writeProfileFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
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

// The supervisor subcommand shares the control-plane boundary: a wildcard
// listen is refused without --supervisor-allow-wildcard-listen and binds with
// it — the enforcement lives in Serve, the flag only opts in.
func TestSupervisorWildcardListenRequiresOptInFlag(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"supervisor", "--spaces", root, "--supervisor-auth", "secret", "--supervisor-listen", "0.0.0.0:0"}); err == nil {
		t.Fatal("wildcard listen accepted without opt-in")
	}
	if err := run(ctx, []string{"supervisor", "--spaces", root, "--supervisor-auth", "secret", "--supervisor-listen", "0.0.0.0:0", "--supervisor-allow-wildcard-listen"}); err != nil {
		t.Fatalf("wildcard listen with opt-in rejected: %v", err)
	}
}

func TestSupervisorComposeEnvLoadsAuthFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "supervisor.auth"), []byte("HUB_SUPERVISOR_AUTH=file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	env := supervisorComposeEnv(dir)
	if !slices.Contains(env, "HUB_SUPERVISOR_AUTH=file-token") {
		t.Fatalf("supervisor.auth not loaded: %#v", env)
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "explicit-token")
	env = supervisorComposeEnv(dir)
	if slices.Contains(env, "HUB_SUPERVISOR_AUTH=file-token") {
		t.Fatal("file token overrode explicit env")
	}
	if err := os.Remove(filepath.Join(dir, "supervisor.auth")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	for _, entry := range supervisorComposeEnv(dir) {
		if strings.HasPrefix(entry, "HUB_SUPERVISOR_AUTH=") && entry != "HUB_SUPERVISOR_AUTH=" {
			t.Fatal("missing file produced a token")
		}
	}
}
