package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestCapabilityCLIProtectedPublicationAndRevoke(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Explicit empty starting policy", Status: toolhub.ActiveStatus}
	write(policy)
	args := []string{"capability", "--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	profile := toolhub.CapabilityProfile{Schema: toolhub.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Explicit empty profile", Status: toolhub.ActiveStatus}
	write(profile)
	args[2] = "profile"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = profile.ProfileID, "dev", 1
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("empty published profile: %v %v", tools, err)
	}
	policy.Revision++
	policy.Status = toolhub.RevokedStatus
	write(policy)
	args[2] = "policy"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); !errors.Is(err, toolhub.ErrUnauthorized) {
		t.Fatalf("existing reader retained revoked policy: %v", err)
	}
	policy.Revision = 1
	policy.Status = toolhub.ActiveStatus
	write(policy)
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), args); !errors.Is(err, toolhub.ErrConflict) {
		t.Fatalf("stale operator input accepted: %v", err)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected input changed registry/history")
	}
}

func TestCapabilityCLIRequiresExplicitConfirmation(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: toolhub.ActiveStatus}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed write committed: %v", err)
	}
	if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unconfirmed write created registry")
	}
	if err := runCapability(append(args, "--issuer", "model", "--confirm")); err == nil {
		t.Fatal("model-issuer confirmation accepted")
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	if _, err := toolhub.Load(storePath); err != nil {
		t.Fatal(err)
	}
	// An operator-supplied confirmation that does not cover this record is void.
	var stored struct {
		Changes []struct {
			Policy *toolhub.CapabilityPolicy `json:"policy"`
		} `json:"capability_changes"`
	}
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored.Changes) != 1 || stored.Changes[0].Policy == nil || stored.Changes[0].Policy.Confirmation == nil {
		t.Fatalf("confirmation not persisted: %v", err)
	}
}

func TestCapabilityCLIGroupAuthoringIsValidationOnly(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "group.json")
	group := toolhub.CapabilityGroup{GroupID: "workspace-reads", Revision: 1, Members: []toolhub.CapabilityRule{
		{CapabilityID: "files.read", ImplementationDigest: "sha256:" + strings.Repeat("a", 64), Action: "read", Resource: "workspace", Limits: toolhub.CapabilityLimits{OutputBytes: 1024, TimeoutSeconds: 5}},
	}}
	body, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "group", "--file", input}); err != nil {
		t.Fatal(err)
	}
	bad := toolhub.CapabilityGroup{GroupID: "workspace-reads", Revision: 0}
	body, _ = json.Marshal(bad)
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "group", "--file", input}); err == nil {
		t.Fatal("malformed group accepted")
	}
}

func TestCapabilityCLIRejectsIncompleteAndMalformedRecords(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	for _, args := range [][]string{nil, {"--unknown"}, {"--kind", "grant"}, {"--kind", "policy", "--file", "x", "--toolhub-store", "x", "extra"}} {
		if err := runCapability(args); err == nil {
			t.Fatalf("bad flags: %v", args)
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "record.json")
	storePath := filepath.Join(dir, "store.json")
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil {
		t.Fatal("missing input accepted")
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `{}`, strings.Repeat(" ", (4<<20)+1)} {
		if err := os.WriteFile(input, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runCapability(args); err == nil {
			t.Fatal("bad input accepted")
		}
		if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("bad input created registry")
		}
	}
	args[1] = "profile"
	if err := os.WriteFile(input, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("bad profile accepted")
	}
	if err := os.WriteFile(storePath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("malformed protected store replaced")
	}
}

// --kind connectors emits the reviewed recommendation manifest without
// touching any store; admission stays opt-in by construction.
func TestCapabilityCLIConnectorsManifest(t *testing.T) {
	if err := runCapability([]string{"--kind", "connectors"}); err != nil {
		t.Fatal(err)
	}
}

// --kind native is the operator-level grant for upstream toolsets: it lists
// the reviewed set, requires --confirm to write a managed settings.yaml, and
// re-validates the file through the exact parser a spawn runs.
func TestCapabilityCLINativeCarveout(t *testing.T) {
	out := captureOutput(t, func() error {
		return runCapability([]string{"--kind", "native"})
	})
	if !strings.Contains(out, "terminal") || !strings.Contains(out, "memory") {
		t.Fatalf("carve-out list missing reviewed names: %s", out)
	}
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.yaml")
	managed := "schema: 1\nuser: alice\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\nmodel: synthetic\nmodel_url: http://model-relay:8318/v1\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nmemory: false\n"
	if err := os.WriteFile(settingsPath, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grant without --confirm prints the diff and refuses.
	args := []string{"--kind", "native", "--settings", settingsPath, "--allow", "terminal,memory"}
	if err := runCapability(args); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed carve-out committed: %v", err)
	}
	if body, _ := os.ReadFile(settingsPath); strings.Contains(string(body), "native_toolsets") {
		t.Fatal("unconfirmed carve-out changed settings")
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.Read(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.NativeToolsets, []string{"memory", "terminal"}) {
		t.Fatalf("grant not applied: %v", settings.NativeToolsets)
	}
	// Replace with a block-list rewrite: the previous inline list is swapped,
	// every other line preserved.
	if err := runCapability([]string{"--kind", "native", "--settings", settingsPath, "--allow", "todo", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	settings, err = stack.Read(settingsPath)
	if err != nil || !slices.Equal(settings.NativeToolsets, []string{"todo"}) {
		t.Fatalf("carve-out swap lost: %v %v", settings.NativeToolsets, err)
	}
	body, _ := os.ReadFile(settingsPath)
	if !strings.Contains(string(body), "capability_mode: managed") {
		t.Fatal("carve-out write dropped settings lines")
	}
	// A non-reviewed name is refused before the file is touched.
	if err := runCapability([]string{"--kind", "native", "--settings", settingsPath, "--allow", "delegation", "--confirm"}); err == nil {
		t.Fatal("bypassing toolset granted")
	}
	// Empty --allow revokes all carve-outs.
	if err := runCapability([]string{"--kind", "native", "--settings", settingsPath, "--allow", "", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	settings, err = stack.Read(settingsPath)
	if err != nil || len(settings.NativeToolsets) != 0 {
		t.Fatalf("revoke left grants: %v %v", settings.NativeToolsets, err)
	}
	// Non-managed settings reject the write before it lands.
	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte("schema: 1\nuser: alice\ntimezone: UTC\nmemory: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "native", "--settings", plain, "--allow", "terminal", "--confirm"}); err == nil {
		t.Fatal("carve-out applied to non-managed settings")
	}
	if body, _ := os.ReadFile(plain); strings.Contains(string(body), "native_toolsets") {
		t.Fatal("rejected carve-out changed the file")
	}
}

// --kind preview diffs an unconfirmed draft against the stored profile using
// the dispatch evaluator and never writes the store.
func TestCapabilityCLIPreview(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: toolhub.ActiveStatus}
	write(policy)
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	profile := toolhub.CapabilityProfile{Schema: toolhub.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed", Status: toolhub.ActiveStatus}
	write(profile)
	args[1] = "profile"
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	// The draft bumps the revision and carries no confirmation: preview must
	// still evaluate it and must not persist anything.
	draft := profile
	draft.Revision++
	write(draft)
	if err := runCapability([]string{"--kind", "preview", "--file", input, "--toolhub-store", storePath}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preview wrote to the protected store")
	}
	// A stale revision previews to the same conflict apply would raise.
	write(profile)
	if err := runCapability([]string{"--kind", "preview", "--file", input, "--toolhub-store", storePath}); err == nil {
		t.Fatal("stale preview admitted")
	}
}
