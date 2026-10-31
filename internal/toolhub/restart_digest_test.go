package toolhub

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func projectionDigest(t *testing.T, store *Store) string {
	t.Helper()
	targets, err := store.ProjectionRevisions()
	if err != nil || len(targets) != 1 {
		t.Fatalf("projection targets=%v err=%v", targets, err)
	}
	return targets[0].Digest
}

func TestManagedSemanticDigestTracksEffectiveAuthority(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	before := projectionDigest(t, store)
	if before == "" {
		t.Error("managed projection has no semantic digest")
	}
	changed := func(label string) {
		t.Helper()
		got := projectionDigest(t, store)
		if got == "" || got == before {
			t.Errorf("%s missing from semantic digest", label)
		}
		before = got
	}
	profile.Reason = "metadata only"
	profile.IssuedAt = profile.IssuedAt.Add(time.Second)
	store.capabilityProfiles[profile.ProfileID] = profile
	if got := projectionDigest(t, store); got != before {
		t.Fatal("profile metadata changed semantic digest")
	}
	policy.Defaults[0].Limits.OutputBytes /= 2
	store.capabilityPolicies[policy.PolicyID] = policy
	changed("effective output limit")
	policy.Defaults[0].Limits.TimeoutSeconds /= 2
	store.capabilityPolicies[policy.PolicyID] = policy
	changed("effective timeout")
	policy.Defaults[0].PathPrefix = "docs/narrowed"
	store.capabilityPolicies[policy.PolicyID] = policy
	changed("effective scope")
	profile.ControlOperations = []string{"status"}
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("control tools")
	profile.Connectors = "personal"
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("connector controls")
	profile.SelfInstall = true
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("self-install authority")
	profile.Generation++
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("runtime generation")
	profile.Environment = "prod"
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("runtime environment")
	policy.Denies = policy.Defaults
	store.capabilityPolicies[policy.PolicyID] = policy
	changed("policy deny")
	auth.Environment, auth.Generation = profile.Environment, profile.Generation
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("denied tools=%v err=%v", tools, err)
	}
	profile.PolicyVersion += "-next"
	store.capabilityProfiles[profile.ProfileID] = profile
	changed("runtime policy binding")
}

func TestTeamSemanticDigestTracksOnlyCallerMembership(t *testing.T) {
	store, definition, team := teamBrokerFixture(t)
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, "alice", definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ShareTeamConnection(team.TeamID, definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	if tools, err := store.ListProjectedTools(aliceAuth()); err != nil || len(tools) == 0 {
		t.Fatalf("fixture has no admitted tools: %v %v", tools, err)
	}
	before := projectionDigest(t, store)
	if before == "" {
		t.Error("team projection has no semantic digest")
	}
	team.IssuedAt = team.IssuedAt.Add(time.Second)
	team.Members = append(team.Members, "carol")
	store.teams[team.TeamID] = team
	if got := projectionDigest(t, store); got != before {
		t.Fatal("foreign membership or metadata changed caller digest")
	}
	team.AllowWrites = true
	store.teams[team.TeamID] = team
	writable := projectionDigest(t, store)
	if writable == "" || writable == before {
		t.Error("caller write authority missing from semantic digest")
	}
	team.Members = []string{"bob", "carol"}
	store.teams[team.TeamID] = team
	if got := projectionDigest(t, store); got == "" || got == writable {
		t.Error("caller membership revocation missing from semantic digest")
	}
	if tools, err := store.ListProjectedTools(aliceAuth()); err != nil || len(tools) != 0 {
		t.Fatalf("revoked tools=%v err=%v", tools, err)
	}
}
