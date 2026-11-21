package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestSelectedHubToolsUsesPublishedToolsets(t *testing.T) {
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := selectedHubTools(toolhub.NewStore(), []byte("core: {files: {via: toolhub}}\n"), docs, ""); err == nil {
		t.Fatal("selected toolset accepted without its registered definition")
	}
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(catalog.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	selected, core, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}}\nextended:\n  memory: {via: toolhub, access: read}\n"), docs, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(selected))
	for _, item := range selected {
		names = append(names, item.Name)
	}
	if !slices.Equal(names, []string{"file_read", "memory_open", "memory_read", "memory_search"}) || len(core) != 1 || core[0] != "file_read" {
		t.Fatalf("published selection/core mismatch: selections=%v core=%v", selected, core)
	}
	selected, _, _, err = selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}, scope: {paths: [/workspace/docs]}, rules: {file_read: {scope: {paths: [/workspace/docs/public]}}}}\n"), docs, "")
	if err != nil || len(selected) != 1 || !slices.Equal(selected[0].PathScopes, []string{"/workspace/docs/public"}) {
		t.Fatalf("selected path scope missing: %v %v", selected, err)
	}
	if _, _, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [project_create]}, scope: {paths: [/workspace/docs]}}\n"), docs, ""); err == nil {
		t.Fatal("path scope admitted a tool without a path argument")
	}
	if _, _, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}, scope: {hosts: [example.test]}}\n"), docs, ""); err == nil {
		t.Fatal("unsupported host scope admitted")
	}
	if _, _, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}, rules: {file_write: {scope: {hosts: [example.test]}}}}\n"), docs, ""); err == nil {
		t.Fatal("unsupported rule for unselected tool admitted")
	}
}

// A publish on a binary whose compiled catalog is newer than the profile's
// active binding must stamp selections against the bound revision — the
// consented catalog generation — never the compiled one. Catalog enable is
// the only transition that moves a profile to a newer definition.
func TestSelectedHubToolsPinsBoundDefinition(t *testing.T) {
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	compiled := catalog.HubToolsDefinition()
	bound := compiled
	bound.Version = "0.0.1"
	if err := store.RegisterDefinition(bound); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDefinition(compiled); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "org-default", PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: bound.DefinitionID, DefinitionVersion: bound.Version, PolicyVersion: "policy-1",
		WorkloadClass: bound.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	selected, _, _, err := selectedHubTools(store, []byte("core:\n  files: {via: toolhub, tools: {include: [file_read]}}\n"), docs, "alice-default")
	if err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(bound)
	for _, selection := range selected {
		if selection.DefinitionVersion != bound.Version || selection.ImplementationDigest != digest {
			t.Fatalf("publish stamped the compiled catalog instead of the bound revision: %+v", selection)
		}
	}
}

func TestConnectorRecoveryOmitsRevokedBinding(t *testing.T) {
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Fatal(err)
	}
	store := toolhub.NewStore()
	now := time.Now().UTC()
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "org-default", PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&profile, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityProfile(profile); err != nil {
		t.Fatal(err)
	}
	tools := []byte("extended:\n  github: {via: mcp:github-mcp-server}\n")
	selected, core, toolsets, deferred, err := selectedConnectorTools(store, tools, docs, "alice-default")
	if err != nil {
		t.Fatalf("pending selection rejected as an authoring error: %v", err)
	}
	if len(selected) != 0 || len(core) != 0 || len(deferred) != 1 || deferred[0] != "github" {
		t.Fatalf("pending selection not deferred: selected=%v core=%v deferred=%v", selected, core, deferred)
	}
	if len(toolsets) != 1 || toolsets[0].Via != "mcp:github-mcp-server" {
		t.Fatalf("pending toolset lost from the profile: %v", toolsets)
	}
}

// fixtureEnvelope is the shared v2 envelope every fixture doc carries.
func fixtureEnvelope(user, revision string) string {
	return "apiVersion: six-passport/v2\nmetadata: {space: " + user + ", environment: dev, revision: " + revision + ", generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\n"
}

// fixtureRevisionDocs returns the six published docs in their enveloped byte
// form — the same shape Revision.Docs and the on-disk passports carry.
func fixtureRevisionDocs(user, revision string) map[string][]byte {
	env := fixtureEnvelope(user, revision)
	docs := map[string]string{
		"stack": "kind: stack\nidentity: {user: " + user + ", infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
	out := make(map[string][]byte, len(docs))
	for name, body := range docs {
		out[name] = []byte(env + body)
	}
	return out
}

// publishFixtureRevision stamps the smallest loadable managed revision: the
// six published docs plus the current pointer the managed read requires.
// Returns the doc bytes so callers can hand the same bundle to Revision.Docs.
func publishFixtureRevision(t *testing.T, dir, user, revision string) map[string][]byte {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", revision)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	docs := fixtureRevisionDocs(user, revision)
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return docs
}

// fixtureRightsDoc is the vocabulary slice the test grants and allow map use.
func fixtureRightsDoc() api.RightsDoc {
	return api.RightsDoc{Release: "fixture", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"workspace": {Description: "fixture", Operations: map[string]api.RightOperation{
			"read": {Subject: "self", Scope: "none", Issuers: []string{"platform", "org", "operator"}, Effect: "read", Description: "fixture"},
		}},
	}}
}

// DeriveSpaceRights must scope the rights ceiling rewrite to the profile the
// revision's tools.binding names (SPEC-0095 R2): a bound dev apply leaves the
// sibling dev profile and the prod profile untouched.
func TestDeriveSpaceRightsScopesToBoundProfile(t *testing.T) {
	now := time.Now().UTC()
	dir := filepath.Join(t.TempDir(), "alice")
	revDocs := publishFixtureRevision(t, dir, "alice", "r-fixture0000001")

	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	store := toolhub.NewStore()
	if err := store.RegisterDefinition(catalog.HubToolsDefinition()); err != nil {
		t.Fatal(err)
	}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: "org-default", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
	if err := api.Confirm(&policy, "operator", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapabilityPolicy(policy); err != nil {
		t.Fatal(err)
	}
	mkProfile := func(id, env string, gen uint64) api.CapabilityProfile {
		p := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: id, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
			Environment: env, Generation: gen, PolicyVersion: "policy-1", PolicyID: "org-default", PolicyRevision: 1,
			Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "fixture", Status: api.ActiveStatus}
		if err := api.Confirm(&p, "operator", now); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, p := range []api.CapabilityProfile{mkProfile("alice-default", "dev", 1), mkProfile("alice-other", "dev", 2), mkProfile("alice-prod", "prod", 1)} {
		if err := store.PutCapabilityProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}

	rights := fixtureRightsDoc()
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	grant := api.NewRightGrant("rightgrant-alice-read", "alice", "workspace", "read", "", "fixture", "operator", now.Add(time.Hour))
	if err := api.Confirm(&grant, grant.GrantedBy, now); err != nil {
		t.Fatal(err)
	}
	if err := gov.GrantRight(grant, rights, now); err != nil {
		t.Fatal(err)
	}

	revDocs["grants"] = []byte(fixtureEnvelope("alice", "r-fixture0000001") + "kind: grants\nallow:\n  workspace: [read]\n")
	rev := passport.Revision{ID: "r-fixture0000001", Docs: revDocs}
	vocab, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	vocab.Rights = rights
	if err := DeriveSpaceRights(dir, "alice", "dev", rev, vocab, now); err != nil {
		t.Fatal(err)
	}

	// The persisted change log is the final state per profile: DeriveRights
	// appends one entry per profile it rewrote.
	body, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Changes []struct {
			Profile *struct {
				ProfileID string `json:"profile_id"`
				Rights    []struct {
					Capability string `json:"capability"`
					Operation  string `json:"operation"`
				} `json:"rights,omitempty"`
			} `json:"profile,omitempty"`
		} `json:"capability_changes"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	// Each fixture PutCapabilityProfile is already one entry; a profile the
	// apply rewrote appears a second time.
	rewritten := map[string]int{}
	for _, change := range snap.Changes {
		if change.Profile != nil {
			rewritten[change.Profile.ProfileID]++
		}
	}
	if rewritten["alice-other"] != 1 {
		t.Fatalf("bound apply rewrote the sibling dev profile (entries=%d, want 1)", rewritten["alice-other"])
	}
	if rewritten["alice-prod"] != 1 {
		t.Fatalf("dev apply rewrote the prod profile (entries=%d, want 1)", rewritten["alice-prod"])
	}
	if rewritten["alice-default"] != 2 {
		t.Fatalf("bound profile rewrite recorded %d entries, want 2", rewritten["alice-default"])
	}
	if err := gov.RevokeRight("rightgrant-alice-read", "operator", "concurrent revoke", now); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSpaceRights(dir, "alice", "dev", "alice-default", "r-fixture0000001", map[string][]string{"workspace": {"read"}}, nil, catalog.Docs{Rights: rights}, now); err != nil {
		t.Fatal(err)
	}
	grants, err := gov.RightGrants()
	if err != nil || len(grants) != 1 || grants[0].Active(now) {
		t.Fatalf("rollback reissued a concurrently revoked right: %+v, %v", grants, err)
	}
	body, err = os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	rewritten = map[string]int{}
	var restoredRights int
	for _, change := range snap.Changes {
		if change.Profile != nil {
			rewritten[change.Profile.ProfileID]++
			if change.Profile.ProfileID == "alice-default" {
				restoredRights = len(change.Profile.Rights)
			}
		}
	}
	if rewritten["alice-default"] != 3 || rewritten["alice-other"] != 1 || rewritten["alice-prod"] != 1 {
		t.Fatalf("first-apply restore touched a sibling profile: %v", rewritten)
	}
	if restoredRights != 0 {
		t.Fatalf("rollback restored a revoked right into the profile ceiling: %d", restoredRights)
	}
}
