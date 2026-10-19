package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S7.1: a Team is an operator-published, confirmed record reusing the
// capability-policy confirmation machinery; membership is evaluated per call.
func TestTeamRecordConfirmedAndStored(t *testing.T) {
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if team.Confirmation == nil || team.Confirmation.Digest == "" {
		t.Fatal("team not confirmed")
	}
	store := NewStore()
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	got, ok := store.TeamFor("platform")
	if !ok || len(got.Members) != 2 || got.Confirmation == nil {
		t.Fatalf("stored team=%+v ok=%v", got, ok)
	}
	// A team owner ref never matches an envelope directly — membership is
	// evaluated against the stored record on every call.
	if (api.OwnerRef{Type: api.TeamOwner, ID: "platform"}).Matches(aliceAuth()) {
		t.Fatal("team owner must not match an envelope directly")
	}
	unconfirmed := api.Team{Schema: api.SchemaVersion, TeamID: "rogue", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", Status: api.ActiveStatus}
	if err := store.PutTeam(unconfirmed); err == nil {
		t.Fatal("unconfirmed team accepted")
	}
}

// S7.1: a team is durable — a store restart must restore the confirmed record
// so membership evaluation keeps working across hub restarts.
func TestTeamRecordPersistsAcrossLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := NewStore()
	store.path = path
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 2, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.TeamFor("platform")
	if !ok || got.Revision != 2 || len(got.Members) != 2 || got.Confirmation == nil {
		t.Fatalf("loaded team=%+v ok=%v", got, ok)
	}
}

// S7.1: a live store picks up team records published by another writer —
// hubctl-published teams must reach a running hub on the next Reload.
func TestTeamRecordAppearsOnReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	live := NewStore()
	live.path = path
	if err := live.Save(path); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	writer, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	got, ok := live.TeamFor("platform")
	if !ok || got.TeamID != "platform" {
		t.Fatalf("live team=%+v ok=%v", got, ok)
	}
}

// S7.1: a team revision is monotonic — a republish must carry a higher
// revision so a stale record cannot downgrade membership.
func TestTeamRevisionIsMonotonic(t *testing.T) {
	store := NewStore()
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 2, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	stale := team
	stale.Members = []string{"mallory"}
	if err := api.Confirm(&stale, "", team.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(stale); err == nil {
		t.Fatal("stale revision accepted")
	}
	got, _ := store.TeamFor("platform")
	if len(got.Members) != 1 || got.Members[0] != "alice" {
		t.Fatalf("team=%+v", got)
	}
}

// S7.2: shareable:connection is only valid on a stateless recipe — sharing
// one workload between members is undefined for durable state.
func TestShareableConnectionRequiresStatelessRecipe(t *testing.T) {
	stateful := api.Recipe{
		Subject: api.RecipeSubject{Kind: api.SubjectRemoteMCP, Ref: "https://mcp.acme.example/x"},
		Launch:  api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.acme.example/x"},
		State:   "durable", Shareable: "connection", Trust: api.TrustT1,
	}
	if err := stateful.Validate(); err == nil {
		t.Fatal("stateful shareable:connection recipe accepted")
	}
	stateless := stateful
	stateless.State = "none"
	if err := stateless.Validate(); err != nil {
		t.Fatalf("stateless shareable:connection recipe rejected: %v", err)
	}
	// shareable:credential keeps durable state — each member runs an
	// isolated workload and only the credential reference is shared.
	credential := stateful
	credential.Shareable = "credential"
	if err := credential.Validate(); err != nil {
		t.Fatalf("stateful shareable:credential recipe rejected: %v", err)
	}
	// Unknown shareable values fail closed.
	unknown := stateless
	unknown.Shareable = "team"
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown shareable mode accepted")
	}
}

// S7.3: a member's own binding on a team-owned connection resolves through
// membership, evaluated per call — not through the connection owner ref.
func TestTeamMemberBindingResolvesThroughMembership(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("team-conn", 1), ConnectionID: "team-conn", Revision: 1, Backend: "local", Locator: "local://team/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "team-conn", Owner: api.OwnerRef{Type: api.TeamOwner, ID: "platform"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: "policy-1", WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	effective, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatalf("member binding rejected: %v", err)
	}
	if effective.Connection == nil || effective.Connection.Owner.Type != api.TeamOwner {
		t.Fatalf("effective=%+v", effective.Connection)
	}
	// shareable:credential keeps per-member workloads: the same team
	// credential reference serves each member under an isolated workload id.
	bobBinding := binding
	bobBinding.PrincipalID, bobBinding.ContextID, bobBinding.RuntimeID = "bob", "bob", "runtime-bob"
	bobBinding.ToolBindingID = api.DeterministicBindingID(bobBinding.PrincipalID, bobBinding.ContextID, bobBinding.RuntimeID, bobBinding.DefinitionID, bobBinding.DefinitionVersion, bobBinding.ConnectionID, bobBinding.CredentialRefID)
	if err := store.PutBinding(bobBinding); err != nil {
		t.Fatal(err)
	}
	bob, err := store.Resolve(bobAuth(), bobBinding.ToolBindingID)
	if err != nil {
		t.Fatalf("second member binding rejected: %v", err)
	}
	if effective.WorkloadID == bob.WorkloadID {
		t.Fatalf("shareable:credential must isolate member workloads: %q", effective.WorkloadID)
	}
	if effective.Credential == nil || bob.Credential == nil || effective.Credential.CredentialRefID != bob.Credential.CredentialRefID {
		t.Fatal("shareable:credential must share one credential reference")
	}
	// Membership is per call: removing alice from the team denies her next
	// resolve without touching her stored binding first.
	updated := team
	updated.Members = []string{"bob"}
	updated.Revision = 2
	if err := api.Confirm(&updated, "", team.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(aliceAuth(), binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("removed member resolve = %v", err)
	}
	if _, err := store.Resolve(bobAuth(), bobBinding.ToolBindingID); err != nil {
		t.Fatalf("remaining member resolve rejected: %v", err)
	}
}

// S7.3: a member's enable converges on the team connection — the matcher must
// see team-owned connections so the member gets an own binding, not a second
// connection or an owner denial.
func TestTeamMemberEnableBindsTeamConnection(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("team-conn", 1), ConnectionID: "team-conn", Revision: 1, Backend: "local", Locator: "local://team/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "team-conn", Owner: api.OwnerRef{Type: api.TeamOwner, ID: "platform"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("member enable rejected: %v", err)
	}
	if binding.ConnectionID != "team-conn" || binding.PrincipalID != "alice" {
		t.Fatalf("binding=%+v", binding)
	}
	effective, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatalf("member resolve rejected: %v", err)
	}
	if effective.Connection == nil || effective.Connection.Owner.Type != api.TeamOwner {
		t.Fatalf("effective=%+v", effective.Connection)
	}
	// A non-member enable on the same definition denies: no team coverage,
	// no own connection.
	if _, err := store.Enable(identity.TelegramEnvelope("carol", 9, "runtime-carol", "policy-1"), definition.DefinitionID, definition.Version); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("non-member enable = %v", err)
	}
}

// S7.4: write-effect tools stay hidden from members unless the operator
// enabled writes on the team — a shared credential must not hand every
// member the write surface by default.
func TestTeamMemberWritesGateOnAllowWrites(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("team-conn", 1), ConnectionID: "team-conn", Revision: 1, Backend: "local", Locator: "local://team/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "team-conn", Owner: api.OwnerRef{Type: api.TeamOwner, ID: "platform"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := store.ListProjectedTools(aliceAuth())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	write := api.ProjectedToolName(definition.DefinitionID, definition.Version, "send")
	if names[write] {
		t.Fatal("write tool projected without operator allow")
	}
	if !names[api.ProjectedToolName(definition.DefinitionID, definition.Version, "search")] {
		t.Fatal("read tool missing from member projection")
	}
	if _, _, err := store.ResolveProjectedTool(aliceAuth(), write); !errors.Is(err, api.ErrNotFound) && !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("hidden write tool callable: %v", err)
	}
	// Operator enables writes team-wide: members project the write tool on
	// the next list — the binding itself is untouched.
	updated := team
	updated.Revision = 2
	updated.AllowWrites = true
	if err := api.Confirm(&updated, "", team.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(updated); err != nil {
		t.Fatal(err)
	}
	tools, err = store.ListProjectedTools(aliceAuth())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		found = found || tool.Name == write
	}
	if !found {
		t.Fatal("write tool still hidden after allow-writes")
	}
	_ = binding
	// Or the operator names one member: bob gets writes, alice stays read-only.
	named := team
	named.Revision = 3
	named.WriteMembers = []string{"bob"}
	if err := api.Confirm(&named, "", team.IssuedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(named); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	bobTools, err := store.ListProjectedTools(bobAuth())
	if err != nil {
		t.Fatal(err)
	}
	bobWrite := false
	for _, tool := range bobTools {
		bobWrite = bobWrite || tool.Name == write
	}
	if !bobWrite {
		t.Fatal("named member did not gain the write tool")
	}
	aliceTools, err := store.ListProjectedTools(aliceAuth())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range aliceTools {
		if tool.Name == write {
			t.Fatal("unnamed member sees the write tool")
		}
	}
}

// S7.5: the operator shares a connector into a team once — one team-owned
// connection and credential reference, members converge on it via Enable.
func TestShareTeamConnectionPublishesTeamOwnedRecords(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatalf("share rejected: %v", err)
	}
	if connection.Owner.Type != api.TeamOwner || connection.Owner.ID != "platform" || connection.Revision != 1 {
		t.Fatalf("connection=%+v", connection)
	}
	if reference.ConnectionID != connection.ConnectionID || reference.Revision != 1 {
		t.Fatalf("reference=%+v", reference)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil || binding.ConnectionID != connection.ConnectionID {
		t.Fatalf("member enable=%v binding=%+v", err, binding)
	}
	// share is rejected for a non-shareable definition and a missing team.
	plain := remoteDefinition()
	plain.DefinitionID = "plain-mcp"
	if err := store.RegisterDefinition(plain); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ShareTeamConnection("platform", plain.DefinitionID, plain.Version, "local", "local://team/2", []string{"GOOGLE_TOKEN"}); err == nil {
		t.Fatal("non-shareable definition shared")
	}
	if _, _, err := store.ShareTeamConnection("ghost", definition.DefinitionID, definition.Version, "local", "local://team/3", []string{"GOOGLE_TOKEN"}); err == nil {
		t.Fatal("missing team shared")
	}
}

// S7.5: shareable:connection lands exactly one workload for every member —
// the shared class already scopes the workload id to the definition alone.
func TestShareableConnectionYieldsOneWorkload(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Class = api.Shared
	definition.Workload.Shareable = "connection"
	definition.Workload.Sharable = true
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, _, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatalf("shareable:connection rejected: %v", err)
	}
	alice, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("alice enable: %v", err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatalf("bob enable: %v", err)
	}
	aliceEff, err := store.Resolve(aliceAuth(), alice.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	bobEff, err := store.Resolve(bobAuth(), bob.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceEff.WorkloadID == "" || aliceEff.WorkloadID != bobEff.WorkloadID {
		t.Fatalf("shareable:connection must yield one workload: %q vs %q", aliceEff.WorkloadID, bobEff.WorkloadID)
	}
	if aliceEff.Credential.CredentialRefID != bobEff.Credential.CredentialRefID {
		t.Fatal("shareable:connection must share one credential reference")
	}
	_ = connection
}

// S7.5: operator rotation on the team connection needs no member reconnect —
// the next call picks the new revision while an in-flight call already
// resolved on the old one keeps its pinned credential.
func TestTeamRotationPicksNewRevisionWithoutReconnect(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	// In-flight call resolves under the old revision before rotation lands.
	inflight, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.RotateCredential(connection.ConnectionID, "local", "local://team/2", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatalf("operator rotate rejected: %v", err)
	}
	if next.Revision != connection.Revision+1 || reference.Revision != 1 {
		t.Fatalf("rotation reference=%+v", next)
	}
	// The same binding resolves the new revision — no member reconnect.
	fresh, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatalf("member call after rotate rejected: %v", err)
	}
	if fresh.Connection.Revision != next.Revision || fresh.Credential.Locator != "local://team/2" {
		t.Fatalf("post-rotate effective=%+v %+v", fresh.Connection, fresh.Credential)
	}
	// The in-flight resolution keeps its original credential revision.
	if inflight.Credential == nil || inflight.Credential.Revision != 1 || inflight.Credential.Locator != "local://team/1" {
		t.Fatalf("in-flight effective mutated: %+v", inflight.Credential)
	}
	// A disable/enable status flip bumps the connection revision without a
	// credential rotation — member calls must not go stale over it.
	if err := store.SetConnectionStatus(connection.ConnectionID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(aliceAuth(), binding.ToolBindingID); err == nil {
		t.Fatal("member call passed on disabled team connection")
	}
	if err := store.SetConnectionStatus(connection.ConnectionID, api.ActiveStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(aliceAuth(), binding.ToolBindingID); err != nil {
		t.Fatalf("member call after re-enable rejected: %v", err)
	}
}

// S7.5/S7.6: a member removing their own binding drops only their projection —
// the team-owned connection and credential reference survive even when the
// removed binding was the last one referencing them.
func TestTeamMemberRemoveKeepsSharedRows(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBindingRecords(binding.ToolBindingID); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	_, connOK := store.connections[connection.ConnectionID]
	_, refOK := store.credentials[reference.CredentialRefID]
	_, bindingOK := store.bindings[binding.ToolBindingID]
	store.mu.RUnlock()
	if bindingOK {
		t.Fatal("member binding survived remove")
	}
	if !connOK || !refOK {
		t.Fatalf("member remove dropped team-owned rows: conn=%v ref=%v", connOK, refOK)
	}
}

// S7.5: operator revoke cuts member calls immediately; DropTeamConnection is
// the operator remove — every member binding plus the shared rows go away.
func TestTeamOperatorRemovePurgesSharedRows(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionStatus(connection.ConnectionID, api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if tools, err := store.ListProjectedTools(aliceAuth()); err != nil || len(tools) != 0 {
		t.Fatalf("alice projection after revoke: %v %v", tools, err)
	}
	if err := store.DropTeamConnection(connection.ConnectionID); err != nil {
		t.Fatal(err)
	}
	for _, bindingID := range []string{alice.ToolBindingID, bob.ToolBindingID} {
		if _, err := store.Resolve(aliceAuth(), bindingID); err == nil {
			t.Fatal("removed member binding still resolves")
		}
	}
	store.mu.RLock()
	_, connOK := store.connections[connection.ConnectionID]
	_, refOK := store.credentials[reference.CredentialRefID]
	bindingRefs := 0
	for _, b := range store.bindings {
		if b.ConnectionID == connection.ConnectionID {
			bindingRefs++
		}
	}
	store.mu.RUnlock()
	if connOK || refOK || bindingRefs != 0 {
		t.Fatalf("shared rows survived operator remove: conn=%v ref=%v bindings=%d", connOK, refOK, bindingRefs)
	}
}

// S7.7: the call audit records the human sender carried by the channel
// JobEnvelope (the transport identity), plus team/connection/binding
// attribution — not merely the session principal.
func TestTeamCallAuditAttributesSenderAndTeam(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, _, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]string
	gateway := &Gateway{Store: store,
		AuditWrite: func(_ string, fields map[string]string) error {
			records = append(records, fields)
			return nil
		},
		Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
			return api.CredentialInjection{}, nil
		},
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		}),
	}
	name := api.ProjectedToolName(definition.DefinitionID, definition.Version, "search")
	if _, err := gateway.CallAuthorized(t.Context(), aliceAuth(), name, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Fatal("no audit records")
	}
	allow := records[len(records)-1]
	if allow["sender_id"] != aliceAuth().ExternalIdentityID {
		t.Fatalf("audit sender_id=%q, want the JobEnvelope sender %q", allow["sender_id"], aliceAuth().ExternalIdentityID)
	}
	if allow["team_id"] != "platform" {
		t.Fatalf("audit team_id=%q, want platform", allow["team_id"])
	}
	if allow["connection_id"] != connection.ConnectionID || allow["binding_id"] != binding.ToolBindingID {
		t.Fatalf("audit attribution connection=%q binding=%q", allow["connection_id"], allow["binding_id"])
	}
}

// S7.8: connector_list surfaces team connections with a team badge so the
// member can distinguish shared resources from personal ones.
func TestTeamConnectorListBadge(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonalTeam
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	definition := remoteDefinition()
	definition.DefinitionID = "team-def"
	definition.Workload.Shareable = "credential"
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, _, err := s.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	var teamCandidate *api.OperationCandidate
	for i := range result.Candidates {
		if result.Candidates[i].ID == connection.ConnectionID {
			teamCandidate = &result.Candidates[i]
		}
	}
	if teamCandidate == nil {
		t.Fatalf("team connection missing from connector_list: %+v", result.Candidates)
	}
	if teamCandidate.Badge != "team" {
		t.Fatalf("team connection lacks the team badge: %+v", *teamCandidate)
	}
}

// S7.8: a member's lifecycle verbs on a team connector return the stable
// not_permitted code — members never mutate team-owned resources — and the
// connection stays active for the team.
func TestTeamConnectorMemberOpsNotPermitted(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonalTeam
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	definition := remoteDefinition()
	definition.DefinitionID = "team-def"
	definition.Workload.Shareable = "credential"
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	connection, _, err := s.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "local", "local://team/1", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: s, Now: time.Now}
	for _, tool := range []string{"connector_disable", "connector_reauth", "connector_remove", "connector_allow_writes"} {
		result, err := c.InvokeConnector(context.Background(), auth, tool, map[string]any{"target": connection.ConnectionID})
		if err != nil {
			t.Fatalf("%s returned a transport error instead of not_permitted: %v", tool, err)
		}
		if result.Code != api.OpCodeNotPermitted {
			t.Fatalf("%s on a team connector: code=%q want not_permitted", tool, result.Code)
		}
	}
	s.mu.RLock()
	got := s.connections[connection.ConnectionID]
	s.mu.RUnlock()
	if got.Status != api.ActiveStatus {
		t.Fatalf("member lifecycle call mutated the team connection: %s", got.Status)
	}
}
