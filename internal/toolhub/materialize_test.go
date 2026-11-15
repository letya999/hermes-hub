package toolhub

import (
	"slices"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// pendingConnectorFixture registers a connector definition carrying a full
// capability contract plus the connection/credential records an active
// binding needs, then pins the authored toolset row — via only, no
// selections — onto the managed profile. That is exactly the state a
// deferred `space apply` publishes.
func pendingConnectorFixture(t *testing.T, s *Store, auth identity.Envelope, profile api.CapabilityProfile) (api.ToolDefinition, api.Connection, api.CredentialReference) {
	t.Helper()
	definition := remoteDefinition()
	definition.DefinitionID = "github-mcp-server"
	// The credential below carries GITHUB_TOKEN; the required input must name
	// the same key or Enable's owner-connection match rejects the fixture.
	definition.Credentials = []api.CredentialInput{{Name: "GITHUB_TOKEN", Required: true}}
	definition.Tools = []api.ToolSpec{{
		Name: "repos_list", Effect: api.ReadEffect, CapabilityID: "github.repos",
		Uses: []api.CapabilityUse{{Action: "read", Resource: "repos"}},
	}}
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("github-mcp-server", 1), ConnectionID: "github-mcp-server", Revision: 1, Backend: "local", Locator: "local://alice/github/1", Keys: []string{"GITHUB_TOKEN"}, Status: api.ActiveStatus}
	if err := s.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "github-mcp-server", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := s.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, s, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	profile.Toolsets = append(profile.Toolsets, api.ProfileToolset{Name: "github", Placement: "extended", Via: "mcp:github-mcp-server"})
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	return definition, connection, credential
}

func connectorBinding(definition api.ToolDefinition, connection api.Connection, credential api.CredentialReference, auth identity.Envelope) api.ToolBinding {
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision,
		CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return binding
}

func connectorSelections(s *Store, profileID string) []api.CapabilitySelection {
	var out []api.CapabilitySelection
	for _, sel := range s.capabilityProfiles[profileID].Selections {
		if sel.Toolset == "github" {
			out = append(out, sel)
		}
	}
	return out
}

// An authored connector toolset published without an active binding gains its
// selections the moment the binding activates — no second apply.
func TestPutBindingMaterializesPendingToolset(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition, connection, credential := pendingConnectorFixture(t, s, auth, profile)
	if got := connectorSelections(s, profile.ProfileID); len(got) != 0 {
		t.Fatalf("pending toolset must carry no selections before activation: %+v", got)
	}
	base := s.capabilityProfiles[profile.ProfileID].Revision
	if err := s.PutBinding(connectorBinding(definition, connection, credential, auth)); err != nil {
		t.Fatal(err)
	}
	got := connectorSelections(s, profile.ProfileID)
	if len(got) != 1 {
		t.Fatalf("activation must materialize the authored toolset's tools: %+v", got)
	}
	want := api.ProjectedToolName(definition.DefinitionID, definition.Version, "repos_list")
	if got[0].Name != want || got[0].ConnectionID != connection.ConnectionID || got[0].CapabilityID != "github.repos" {
		t.Fatalf("materialized selection mismatch: %+v", got[0])
	}
	// The profile write rode the journal — replay must see the same revision.
	stored := s.capabilityProfiles[profile.ProfileID]
	if stored.Revision != base+1 {
		t.Fatalf("materialization must bump the profile revision once: %d -> %d", base, stored.Revision)
	}
}

// Re-running activation with the same binding is a no-op: identical
// selections, no extra revision bump, no duplicated journal writes.
func TestPutBindingMaterializationIsIdempotent(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition, connection, credential := pendingConnectorFixture(t, s, auth, profile)
	binding := connectorBinding(definition, connection, credential, auth)
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	revision := s.capabilityProfiles[profile.ProfileID].Revision
	changes := len(s.capabilityChanges)
	// Identical PutBinding is a no-op per binding immutability — but replay
	// paths may re-run the write; assert the materializer itself stays put.
	s.materializePendingSelectionsLocked(binding)
	if got := s.capabilityProfiles[profile.ProfileID].Revision; got != revision {
		t.Fatalf("idempotent materialization must not bump the revision: %d -> %d", revision, got)
	}
	if len(s.capabilityChanges) != changes {
		t.Fatal("idempotent materialization must not append journal changes")
	}
	if got := connectorSelections(s, profile.ProfileID); len(got) != 1 {
		t.Fatalf("idempotent materialization must not duplicate selections: %+v", got)
	}
}

// The inventory tells the model an authored connector set exists even before
// any tool is callable: the toolsets row carries state:pending.
func TestToolsListMarksPendingConnectorToolset(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	pendingConnectorFixture(t, s, auth, profile)
	g := &Gateway{Store: s}
	body, err := g.toolsList(auth, "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	toolsets, _ := body["toolsets"].(map[string]any)
	extended, _ := toolsets["extended"].([]map[string]any)
	var row map[string]any
	for _, record := range extended {
		if record["name"] == "github" {
			row = record
		}
	}
	if row == nil {
		t.Fatalf("authored pending toolset must appear in inventory: %+v", toolsets)
	}
	if row["state"] != "pending" || row["tools_total"] != 0 {
		t.Fatalf("pending connector toolset must report state:pending with zero tools: %+v", row)
	}
}

// The production activation path — Enable/EnableReady — must materialize the
// authored toolset's selections too, not only the PutBinding fixture path.
// Regression: onboarding enabled github-mcp-server through EnableReady while
// the materializer only ran inside PutBinding, leaving the toolset pending
// forever despite a ready connection and active binding.
func TestEnableMaterializesPendingToolset(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition, _, _ := pendingConnectorFixture(t, s, auth, profile)
	if got := connectorSelections(s, profile.ProfileID); len(got) != 0 {
		t.Fatalf("pending toolset must carry no selections before activation: %+v", got)
	}
	binding, err := s.Enable(auth, definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Status != api.ActiveStatus {
		t.Fatalf("enable must activate the binding: %+v", binding)
	}
	got := connectorSelections(s, profile.ProfileID)
	if len(got) != 1 {
		t.Fatalf("Enable must materialize the authored toolset's tools: %+v", got)
	}
	if got[0].ConnectionID != binding.ConnectionID || got[0].CapabilityID != "github.repos" {
		t.Fatalf("materialized selection mismatch: %+v", got[0])
	}
	// The inventory must report the toolset live — not pending — now that
	// the binding's tools project into the profile.
	g := &Gateway{Store: s}
	body, err := g.toolsList(auth, "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	toolsets, _ := body["toolsets"].(map[string]any)
	extended, _ := toolsets["extended"].([]map[string]any)
	for _, record := range extended {
		if record["name"] == "github" && record["state"] == "pending" {
			t.Fatalf("activated toolset must not report pending: %+v", record)
		}
	}
}

// A store that committed an active binding without running the materializer —
// written before the activation hook existed, or replayed from an older
// snapshot — heals through the periodic reconciliation sweep, not through a
// re-onboard.
func TestMaterializePendingSelectionsSweepHealsDriftedStore(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition, connection, credential := pendingConnectorFixture(t, s, auth, profile)
	// Drift simulation: the binding is committed directly, bypassing every
	// activation path — exactly the state the github-mcp-server store was
	// left in after EnableReady skipped the materializer.
	binding := connectorBinding(definition, connection, credential, auth)
	s.mu.Lock()
	s.bindings[binding.ToolBindingID] = binding
	s.mu.Unlock()
	if got := connectorSelections(s, profile.ProfileID); len(got) != 0 {
		t.Fatalf("drifted store must show the toolset pending: %+v", got)
	}
	s.MaterializePendingSelections()
	got := connectorSelections(s, profile.ProfileID)
	if len(got) != 1 || got[0].ConnectionID != connection.ConnectionID {
		t.Fatalf("sweep must materialize the authored toolset's tools: %+v", got)
	}
	// A converged store stays put: the sweep is idempotent, no revision bump.
	revision := s.capabilityProfiles[profile.ProfileID].Revision
	changes := len(s.capabilityChanges)
	s.MaterializePendingSelections()
	if got := s.capabilityProfiles[profile.ProfileID].Revision; got != revision {
		t.Fatalf("idempotent sweep must not bump the revision: %d -> %d", revision, got)
	}
	if len(s.capabilityChanges) != changes {
		t.Fatal("idempotent sweep must not append journal changes")
	}
}

// A re-onboarded connection superseding the old binding re-pins the toolset's
// selections to the new connection without a passport publish.
func TestPutBindingRematerializesOnConnectionSwap(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition, connection, credential := pendingConnectorFixture(t, s, auth, profile)
	if err := s.PutBinding(connectorBinding(definition, connection, credential, auth)); err != nil {
		t.Fatal(err)
	}
	credential2 := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("github-mcp-server-2", 1), ConnectionID: "github-mcp-server-2", Revision: 1, Backend: "local", Locator: "local://alice/github/2", Keys: []string{"GITHUB_TOKEN"}, Status: api.ActiveStatus}
	if err := s.PutCredentialReference(credential2); err != nil {
		t.Fatal(err)
	}
	connection2 := api.Connection{Schema: api.SchemaVersion, ConnectionID: "github-mcp-server-2", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: credential2.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := s.PutConnection(connection2); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBinding(connectorBinding(definition, connection2, credential2, auth)); err != nil {
		t.Fatal(err)
	}
	got := connectorSelections(s, profile.ProfileID)
	if len(got) != 1 || got[0].ConnectionID != connection2.ConnectionID {
		t.Fatalf("stale selections must re-pin to the new connection: %+v", got)
	}
	if slices.ContainsFunc(s.capabilityProfiles[profile.ProfileID].Selections, func(sel api.CapabilitySelection) bool {
		return sel.ConnectionID == connection.ConnectionID
	}) {
		t.Fatal("superseded connection's selections must be removed")
	}
}
