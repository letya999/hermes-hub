package toolhub

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestConnectorStatusRechecksTeamMembershipAfterListing(t *testing.T) {
	store, definition, team := teamBrokerFixture(t)
	connection, _, err := store.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "credential_team", "team-context", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store}
	listed, err := c.connectorList(aliceAuth())
	if err != nil || len(listed.Candidates) != 1 {
		t.Fatalf("team connection not visible before removal: %+v %v", listed, err)
	}
	team.Members, team.Revision = []string{"bob"}, team.Revision+1
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	status, err := c.connectorStatus(t.Context(), aliceAuth(), connection.ConnectionID, "")
	if !errors.Is(err, api.ErrNotFound) || len(status.Candidates) != 0 {
		t.Fatalf("removed member retained connection visibility: %+v %v", status, err)
	}
}

func TestConnectorStatusReadsTeamConnectionWithoutGrantingLifecycleAuthority(t *testing.T) {
	store, definition, team := teamBrokerFixture(t)
	connection, _, err := store.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "credential_team", "team-context", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	c := &ControlPlane{Store: store}
	listed, err := c.connectorList(auth)
	if err != nil || len(listed.Candidates) != 1 {
		t.Fatalf("team inventory: %+v %v", listed, err)
	}
	status, err := c.connectorStatus(t.Context(), auth, connection.ConnectionID, "")
	if err != nil || len(status.Candidates) != 1 || !reflect.DeepEqual(status.Candidates[0], listed.Candidates[0]) {
		t.Fatalf("team status unavailable or disagrees with inventory: %+v %v", status, err)
	}
	if _, err := store.ResolveConnectorTarget(auth, connection.ConnectionID); !errors.Is(err, ErrTeamConnectionTarget) {
		t.Fatalf("read access changed lifecycle authorization: %v", err)
	}
}

func TestConnectorStatusReadsOwnedConnectionWithoutOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	if err := store.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store}
	listed, err := c.connectorList(auth)
	if err != nil || len(listed.Candidates) != 1 {
		t.Fatalf("inventory: %+v %v", listed, err)
	}
	status, err := c.connectorStatus(t.Context(), auth, listed.Candidates[0].ID, "")
	if err != nil || status.State != api.OpStateDone || len(status.Candidates) != 1 {
		t.Fatalf("listed connection cannot be inspected: %+v %v", status, err)
	}
	if !reflect.DeepEqual(status.Candidates[0], listed.Candidates[0]) {
		t.Fatalf("status disagrees with inventory: %+v vs %+v", status.Candidates, listed.Candidates)
	}
}

func TestConnectorListIncludesOwnedConnectionWithoutOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection, ok := store.connectionFor("google-work")
	if !ok {
		t.Fatal("fixture connection missing")
	}
	if err := store.MarkDegraded(connection.ConnectionID); err != nil {
		t.Fatal(err)
	}
	result, err := (&ControlPlane{Store: store}).connectorList(auth)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("existing connection hidden without onboarding: %+v %v", result, err)
	}
	got := result.Candidates[0]
	if got.ID != connection.ConnectionID || got.ConnectionID != connection.ConnectionID || got.DefinitionID != connection.DefinitionID || got.ConnectionStatus != api.DegradedStatus || got.OnboardingPhase != "" {
		t.Fatalf("connection-only facts incorrect: %+v", got)
	}
	definition, err := store.definitionForConnection(connection)
	if err != nil {
		t.Fatal(err)
	}
	if got.Transport != string(definition.Transport) {
		t.Fatalf("connection format omitted: %+v", got)
	}
	if got.Owner.Type != connection.Owner.Type || got.Owner.ID != connection.Owner.ID {
		t.Fatalf("connection owner omitted: %+v", got)
	}
}

func TestConnectorListCarriesCredentialCheckEvidence(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	connection := store.connections["google-work"]
	connection.CredentialVisibility = "unknown"
	connection.HealthCause = "credential_visibility_unknown"
	connection.CredentialCheckedAt = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	store.connections[connection.ConnectionID] = connection
	result, err := (&ControlPlane{Store: store}).connectorList(auth)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("inventory: %+v %v", result, err)
	}
	got := result.Candidates[0]
	if got.CredentialVisibility != connection.CredentialVisibility || got.HealthCause != connection.HealthCause || !got.CredentialCheckedAt.Equal(connection.CredentialCheckedAt) {
		t.Fatalf("credential evidence omitted: %+v", got)
	}
}

func TestConnectorStatusUsesScopedInventoryForOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	c := &ControlPlane{Store: store}
	listed, err := c.connectorList(auth)
	if err != nil || len(listed.Candidates) != 1 {
		t.Fatalf("inventory: %+v %v", listed, err)
	}
	status, err := c.connectorStatus(t.Context(), auth, onboarding.OnboardingID, "")
	if err != nil || len(status.Candidates) != 1 || !reflect.DeepEqual(status.Candidates[0], listed.Candidates[0]) {
		t.Fatalf("onboarding status differs from inventory: %+v %v", status, err)
	}
}

func TestConnectorListSelectsNewestOnboardingWithStableTieBreak(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	original := m13P3EnabledOnboarding(t, store, auth)
	for _, id := range []string{"recent-a", "recent-z", "old-z"} {
		o := original
		o.OnboardingID = id
		o.CreatedAt = original.CreatedAt.Add(time.Hour)
		if id == "old-z" {
			o.CreatedAt = original.CreatedAt.Add(-time.Hour)
		}
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	for range 100 {
		result, err := (&ControlPlane{Store: store}).connectorList(auth)
		if err != nil || len(result.Candidates) != 1 || result.Candidates[0].ID != "recent-z" {
			t.Fatalf("onboarding selection depends on map iteration: %+v %v", result, err)
		}
	}
}

func TestConnectorListDeduplicatesCurrentConnectionWithinCallerScope(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	current := m13P3EnabledOnboarding(t, store, auth)
	connection, _ := store.connectionFor(current.ConnectionID)
	current.CredentialRefID = connection.CredentialRefID
	legacy := current
	legacy.OnboardingID = "legacy-onboarding"
	legacy.CredentialRefID = ""
	legacy.CreatedAt = current.CreatedAt.Add(time.Hour)
	stale := legacy
	stale.OnboardingID = "stale-onboarding"
	stale.CredentialRefID = "old-reference"
	foreign := current
	foreign.OnboardingID = "other-context-onboarding"
	foreign.ContextID = "other-context"
	foreign.CreatedAt = current.CreatedAt.Add(2 * time.Hour)
	pending := m13P3Onboarding(auth, "pending", "github", "", api.PhaseAwaitingCreds)
	for _, o := range []api.Onboarding{current, legacy, stale, foreign, pending} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		result, err := (&ControlPlane{Store: store}).connectorList(auth)
		if err != nil || len(result.Candidates) != 2 {
			t.Fatalf("duplicate or missing inventory entries: %+v %v", result, err)
		}
		found := map[string]bool{}
		for _, candidate := range result.Candidates {
			found[candidate.ID] = true
			if candidate.ID == pending.OnboardingID {
				body, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(body, &fields) != nil {
					t.Fatal("candidate JSON invalid")
				}
				if _, hasOwner := fields["owner"]; hasOwner {
					t.Fatalf("pending onboarding invented connection owner: %s", body)
				}
			}
		}
		if !found[current.OnboardingID] || !found[pending.OnboardingID] {
			t.Fatalf("current connection replaced by history or another context: %+v", result.Candidates)
		}
	}
}

func TestConnectorListDoesNotExposeForeignConnectionThroughStaleOnboarding(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	foreign := api.Connection{Schema: api.SchemaVersion, ConnectionID: "foreign-connection", DefinitionID: onboarding.DefinitionID,
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "bob"}, Revision: 1, Status: api.DegradedStatus}
	if err := store.PutConnection(foreign); err != nil {
		t.Fatal(err)
	}
	onboarding.ConnectionID = foreign.ConnectionID
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	result, err := (&ControlPlane{Store: store}).connectorList(auth)
	if err != nil || len(result.Candidates) != 2 {
		t.Fatalf("owner onboarding or independent owned connection missing: %+v %v", result, err)
	}
	found := false
	for _, got := range result.Candidates {
		if got.ID == onboarding.OnboardingID {
			found = true
			if got.ConnectionID != "" || got.ConnectionStatus != "" {
				t.Fatalf("foreign connection metadata disclosed: %+v", got)
			}
		}
		if got.ConnectionID == foreign.ConnectionID {
			t.Fatal("foreign connection included")
		}
	}
	if !found {
		t.Fatal("owner onboarding omitted")
	}
}

func TestConnectorListReportsTeamConnectionStateWithoutPersonalOnboarding(t *testing.T) {
	store, definition, team := teamBrokerFixture(t)
	connection, _, err := store.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "credential_team", "team-context", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDegraded(connection.ConnectionID); err != nil {
		t.Fatal(err)
	}
	result, err := (&ControlPlane{Store: store}).connectorList(aliceAuth())
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("team inventory: %+v %v", result, err)
	}
	got := result.Candidates[0]
	if got.ConnectionID != connection.ConnectionID || got.DefinitionID != definition.DefinitionID || got.ConnectionStatus != api.DegradedStatus || got.Badge != "team" || got.OnboardingPhase != "" {
		t.Fatalf("team connection state omitted or invented: %+v", got)
	}
}

func TestConnectorListSeparatesTransportConnectionAndOnboardingState(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	if err := store.MarkDegraded(onboarding.ConnectionID); err != nil {
		t.Fatal(err)
	}
	definition, err := store.Definition(onboarding.DefinitionID, onboarding.DefinitionVersion)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&ControlPlane{Store: store}).connectorList(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected one connector: %+v", result)
	}
	encoded, err := json.Marshal(result.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"connection_id":     onboarding.ConnectionID,
		"definition_id":     onboarding.DefinitionID,
		"transport":         string(definition.Transport),
		"connection_status": string(api.DegradedStatus),
		"onboarding_phase":  api.PhaseEnabled,
	} {
		if fields[key] != want {
			t.Errorf("%s = %v; want %q", key, fields[key], want)
		}
	}
}

func TestConnectorListExcludesTerminalAndSupersededOnboarding(t *testing.T) {
	store := NewStore()
	auth := aliceAuth()
	current := m13P3Onboarding(auth, "current", "github", "", api.PhaseAwaitingCreds)
	removed := m13P3Onboarding(auth, "removed", "github", "", api.PhaseRemoved)
	revoked := m13P3Onboarding(auth, "revoked", "github", "", api.PhaseRevoked)
	superseded := m13P3Onboarding(auth, "superseded", "github", "", api.PhaseEnabled)
	superseded.SupersededBy = current.OnboardingID
	for _, o := range []api.Onboarding{current, removed, revoked, superseded} {
		if err := store.PutOnboarding(o); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&ControlPlane{Store: store}).connectorList(auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].ID != current.OnboardingID {
		t.Fatalf("history reported as current connectors: %+v", result.Candidates)
	}
	// Listing is a read: history must remain available for operation lookup.
	if _, err := store.OnboardingFor(auth, removed.OnboardingID); err != nil {
		t.Fatalf("listing removed history: %v", err)
	}
}
