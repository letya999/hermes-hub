package main

import (
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestAgentToolsRegistrationSupersedesStaleBindingAndNotifies(t *testing.T) {
	freshPath := filepath.Join(t.TempDir(), "fresh.json")
	if err := runCapability([]string{"--kind", "agent-tools", "--toolhub-store", freshPath, "--principal", "bob", "--context", "bob", "--runtime", "bob", "--policy-version", "policy-new"}); err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, err := toolhub.Load(freshPath); err != nil {
		t.Fatalf("new registry was not saved: %v", err)
	}
	storePath := filepath.Join(t.TempDir(), "store.json")
	store := toolhub.NewStore()
	previous := catalog.HubToolsDefinition()
	previous.Version = "1.3.0"
	previous.Tools = slices.DeleteFunc(previous.Tools, func(tool api.ToolSpec) bool { return tool.Name == "routine_get" })
	if err := store.RegisterDefinition(previous); err != nil {
		t.Fatal(err)
	}
	stale := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
		DefinitionID: previous.DefinitionID, DefinitionVersion: previous.Version, PolicyVersion: "policy-old",
		WorkloadClass: previous.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 5}
	if err := store.PutBinding(stale); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	args := []string{"--kind", "agent-tools", "--toolhub-store", storePath, "--principal", "alice", "--context", "alice", "--runtime", "alice", "--policy-version", "policy-new"}
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-new"}
	revision, err := store.ProjectionRevision(auth)
	if err != nil || revision <= stale.ProjectionRevision {
		t.Fatalf("projection did not advance: revision=%d err=%v", revision, err)
	}
	tools, err := store.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tools, func(v api.ProjectedTool) bool { return v.Tool.Name == "inbox_fetch" }) {
		t.Fatal("current inbox tool missing from projection")
	}
	if err := runCapability(args); err != nil {
		t.Fatalf("repeat registration: %v", err)
	}
	store, err = toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := store.ProjectionRevision(auth); err != nil || again != revision {
		t.Fatalf("idempotent registration changed projection: %d -> %d: %v", revision, again, err)
	}
}

func TestAgentToolsRegistrationRetriesStaleStoreSnapshot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "store.db")
	seed := toolhub.NewStore()
	if err := seed.Save(p); err != nil {
		t.Fatal(err)
	}
	_ = seed.CloseDB()
	previous := loadOperatorStore
	calls := 0
	loadOperatorStore = func(path string) (*toolhub.Store, error) {
		store, err := toolhub.Load(path)
		if err != nil {
			return nil, err
		}
		calls++
		if calls == 1 {
			other, err := toolhub.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			old := catalog.HubToolsDefinition()
			old.Version = "1.3.0"
			if err := other.RegisterDefinition(old); err != nil {
				t.Fatal(err)
			}
			if err := other.Save(path); err != nil {
				t.Fatal(err)
			}
			_ = other.CloseDB()
		}
		return store, nil
	}
	t.Cleanup(func() { loadOperatorStore = previous })
	if err := runCapability([]string{"--kind", "agent-tools", "--toolhub-store", p, "--principal", "alice", "--context", "alice", "--runtime", "alice", "--policy-version", "policy-1"}); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatal("stale registration did not reload the store")
	}
}

func TestAgentToolsExplicitVersionPinsStoredDefinition(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	store := toolhub.NewStore()
	pinned := catalog.HubToolsDefinition()
	pinned.Version = "1.3.0"
	pinned.Tools = slices.DeleteFunc(pinned.Tools, func(tool api.ToolSpec) bool { return tool.Name == "routine_get" })
	if err := store.RegisterDefinition(pinned); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	if err := runCapability([]string{"--kind", "agent-tools", "--version", "1.3.0", "--toolhub-store", storePath, "--principal", "alice", "--context", "alice", "--runtime", "alice", "--policy-version", "policy-new"}); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-new"}
	tools, err := store.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) == 0 {
		t.Fatal("pinned version projected no tools")
	}
	if slices.ContainsFunc(tools, func(v api.ProjectedTool) bool { return v.Tool.Name == "routine_get" }) {
		t.Fatal("compiled catalog leaked into pinned-version projection")
	}
}

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
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Explicit empty starting policy", Status: api.ActiveStatus}
	write(policy)
	args := []string{"capability", "--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Explicit empty profile", Status: api.ActiveStatus}
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
	policy.Status = api.RevokedStatus
	write(policy)
	args[2] = "policy"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("existing reader retained revoked policy: %v", err)
	}
	policy.Revision = 1
	policy.Status = api.ActiveStatus
	write(policy)
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), args); !errors.Is(err, api.ErrConflict) {
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
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: api.ActiveStatus}
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
			Policy *api.CapabilityPolicy `json:"policy"`
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
	group := api.CapabilityGroup{GroupID: "workspace-reads", Revision: 1, Members: []api.CapabilityRule{
		{CapabilityID: "files.read", ImplementationDigest: "sha256:" + strings.Repeat("a", 64), Action: "read", Resource: "workspace", Limits: api.CapabilityLimits{OutputBytes: 1024, TimeoutSeconds: 5}},
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
	bad := api.CapabilityGroup{GroupID: "workspace-reads", Revision: 0}
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

func TestCapabilityRejectsRetiredToolsWriter(t *testing.T) {
	if err := runCapability([]string{"--kind", "tools", "--settings", t.TempDir(), "--set", "web=native", "--confirm"}); err == nil {
		t.Fatal("retired workspace.yaml writer remains available")
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
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed", Status: api.ActiveStatus}
	write(policy)
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath, "--confirm"}
	if err := runCapability(args); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed", Status: api.ActiveStatus}
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
