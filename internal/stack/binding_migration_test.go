package stack

import (
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// migrationFixture renders one space and reports the policy version the
// manifest recorded, so the test can seed bindings under exactly that hash.
func migrationFixture(t *testing.T) (dir, root string, policy string) {
	t.Helper()
	dir, root = t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools = testTools("workspace", "telegram", "web=toolhub")
	settings.Ingress = testIngress("workspace", "telegram")
	if err := saveSpace(dir, settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\nTELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readToolManifest(dir, "prod")
	if err != nil || manifest == nil {
		t.Fatalf("first render wrote no manifest: %v", err)
	}
	return dir, root, manifest.PolicyVersion
}

func migrationDefinition(id string) api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.Shared, Rationale: "stateless shared workload"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func seedMigrationStore(t *testing.T, dir, policy string) {
	t.Helper()
	store := toolhub.NewStore()
	for _, id := range []string{"mig-active", "mig-revoked"} {
		if err := store.RegisterDefinition(migrationDefinition(id)); err != nil {
			t.Fatal(err)
		}
	}
	for _, seed := range []struct {
		definitionID string
		status       api.Status
	}{
		{"mig-active", api.ActiveStatus},
		{"mig-revoked", api.RevokedStatus},
	} {
		binding := api.ToolBinding{
			Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
			DefinitionID: seed.definitionID, DefinitionVersion: "1.0.0", PolicyVersion: policy,
			WorkloadClass: api.Shared, Status: seed.status, Revision: 1, ProjectionRevision: 1,
		}
		if err := store.PutBinding(binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(filepath.Join(dir, "runtime", "toolhub", "store.json")); err != nil {
		t.Fatal(err)
	}
}

func storeBindings(t *testing.T, dir string) (bindings map[string]struct {
	Policy string `json:"policy_version"`
	Status string `json:"status"`
}, changes []struct {
	From string `json:"from_policy"`
	To   string `json:"to_policy"`
}) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "runtime", "toolhub", "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Bindings []struct {
			ID     string `json:"tool_binding_id"`
			Policy string `json:"policy_version"`
			Status string `json:"status"`
		} `json:"bindings"`
		Changes []struct {
			From string `json:"from_policy"`
			To   string `json:"to_policy"`
		} `json:"binding_changes"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	bindings = map[string]struct {
		Policy string `json:"policy_version"`
		Status string `json:"status"`
	}{}
	for _, b := range snapshot.Bindings {
		bindings[b.ID] = struct {
			Policy string `json:"policy_version"`
			Status string `json:"status"`
		}{b.Policy, b.Status}
	}
	return bindings, snapshot.Changes
}

// SPEC-0053 R1.2/A1.1-A1.2: an operator render that rolls the rights hash must
// carry this space's active bindings to the new policy version — `hubctl up`
// and `hubctl build` funnel through RenderEnvironment, so the migrated store
// is what the next deploy serves. Revoked bindings never resurrect.
func TestUpMigratesAfterRightsChange(t *testing.T) {
	dir, root, policyV1 := migrationFixture(t)
	seedMigrationStore(t, dir, policyV1)

	// A rights-bearing change (a newly enabled tool) moves the policy hash;
	// changing only the model must not (that case is pinned by
	// TestPolicyVersionIgnoresNonRightsFields).
	settings, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings.Tools["file"] = ToolEntry{Via: ToolViaNative}
	if err := saveSpace(dir, settings); err != nil {
		t.Fatal(err)
	}
	// Native toolsets are default-deny (SPEC-0083 R3.1): the rights change is
	// only applied when a grant covers the wished section.
	seedGovernance(t, dir, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:file", time.Hour)
	})
	if err := RenderEnvironment(dir, root, "prod"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readToolManifest(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	policyV2 := manifest.PolicyVersion
	if policyV2 == policyV1 {
		t.Fatal("rights change did not roll the policy hash")
	}

	bindings, changes := storeBindings(t, dir)
	activeID := api.DeterministicBindingID("alice", "alice", "alice", "mig-active", "1.0.0", "", "")
	revokedID := api.DeterministicBindingID("alice", "alice", "alice", "mig-revoked", "1.0.0", "", "")
	if got := bindings[activeID]; got.Policy != policyV2 || got.Status != "active" {
		t.Fatalf("active binding not migrated: %+v (want policy %s)", got, policyV2)
	}
	if got := bindings[revokedID]; got.Policy != policyV1 || got.Status != "revoked" {
		t.Fatalf("revoked binding must stay revoked at the old policy: %+v", got)
	}
	if len(changes) != 1 || changes[0].From != policyV1 || changes[0].To != policyV2 {
		t.Fatalf("migration audit missing the transition: %+v", changes)
	}
}
