package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// doctorStore builds a managed store whose profile selections were legal when
// written, then a policy revision shrank the defaults that covered them — the
// same durable gap a publish-time repin leaves, reached through the public
// write path (the write gate only refuses selections the write itself
// introduces).
func doctorStore(t *testing.T, root string) {
	t.Helper()
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source: api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools: []api.ToolSpec{{Name: "search", Effect: api.ReadEffect, CapabilityID: "files.read",
			Uses:        []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}},
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "fixture"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: digest, Action: "read", Resource: "workspace", PathPrefix: "docs",
		Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}}
	now := time.Now().UTC()
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus,
		Ceiling: []api.CapabilityRule{rule}, Defaults: []api.CapabilityRule{rule}}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: policy.PolicyID, PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus,
		Selections: []api.CapabilitySelection{{CapabilityID: rule.CapabilityID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: digest, ToolName: "search", Name: "workspace_read"}}}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	// Narrow the defaults: the carried-over selection stays writable but is
	// now uncovered — the gap doctor must name.
	policy.Revision++
	policy.Defaults = nil
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(filepath.Join(root, "store.json")); err != nil {
		t.Fatal(err)
	}
}

func TestToolhubDoctorWarningsReportsSelectionGap(t *testing.T) {
	// A directory outside spaces/ resolves to itself as the infra space, so
	// the fixture store lands exactly where the doctor reads it.
	dir := t.TempDir()
	doctorStore(t, filepath.Join(dir, "runtime", "toolhub"))
	warnings := toolhubDoctorWarnings(dir, locale.LangEN)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "alice-default") || !strings.Contains(warnings[0], "catalog-enable") {
		t.Fatalf("doctor must name the split profile and its repair command: %v", warnings)
	}
}

func TestToolhubDoctorWarningsSkipsUnmanagedAndConsistent(t *testing.T) {
	if warnings := toolhubDoctorWarnings(t.TempDir(), locale.LangEN); len(warnings) != 0 {
		t.Fatalf("missing store must not warn: %v", warnings)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "runtime", "toolhub")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := toolhub.NewStore().Save(filepath.Join(root, "store.json")); err != nil {
		t.Fatal(err)
	}
	if warnings := toolhubDoctorWarnings(dir, locale.LangEN); len(warnings) != 0 {
		t.Fatalf("consistent store must not warn: %v", warnings)
	}
}
