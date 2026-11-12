package toolhub

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// altDefinition is a second generation of the fixture remote definition: same
// capability contract under a different immutable revision, so its
// implementation digest differs from the rules the fixture policy derived.
// It mirrors the digest-split a publish leaves when it repins selections to a
// catalog revision whose rules were never enabled.
func altDefinition() api.ToolDefinition {
	definition := remoteDefinition()
	definition.Version = "3.0.0"
	definition.Tools[0].CapabilityID = "files.read"
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "read", Resource: "workspace", PathArgument: "path"}}
	definition.Tools[0].ArgumentEquals = map[string]string{"scope": "workspace"}
	definition.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"scope":{"type":"string"}},"required":["path","scope"],"additionalProperties":false}`)
	return definition
}

// digestSplitProfile retargets the fixture profile's selection to the new
// definition revision while every capability rule still pins the old digest.
func digestSplitProfile(t *testing.T, s *Store, profile api.CapabilityProfile) api.CapabilityProfile {
	t.Helper()
	definition := altDefinition()
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(profile)
	var split api.CapabilityProfile
	if err := json.Unmarshal(body, &split); err != nil {
		t.Fatal(err)
	}
	split.Revision++
	split.Selections[0].DefinitionVersion = definition.Version
	split.Selections[0].ImplementationDigest = api.DefinitionDigest(definition)
	return split
}

func TestPutCapabilityProfileRejectsRepinnedSelection(t *testing.T) {
	s, _, policy, profile := managedStore(t)
	split := digestSplitProfile(t, s, profile)
	if err := putProfile(t, s, split); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("selection repinned past rules on another digest must refuse publish: %v", err)
	}
	// Enabling the new catalog revision — extending ceiling and defaults to
	// the new digest — admits the very same selection write.
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, api.CapabilityRule{CapabilityID: "files.read", ImplementationDigest: split.Selections[0].ImplementationDigest,
		Action: "read", Resource: "workspace", ConnectionID: split.Selections[0].ConnectionID, PathPrefix: "docs",
		Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 10}})
	policy.Defaults = append(policy.Defaults, policy.Ceiling[len(policy.Ceiling)-1])
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	split.PolicyRevision = policy.Revision
	if err := putProfile(t, s, split); err != nil {
		t.Fatalf("covered selection must publish: %v", err)
	}
}

// A selection whose tuple no rule governs at any digest is writable: the
// state is suppressed under projection (denial surface + doctor report it)
// but revocation and never-authorized intent are legitimate, unlike a repin
// that abandons rules at another digest.
func TestPutCapabilityProfileAdmitsNeverAuthorizedSelection(t *testing.T) {
	s, _, _, profile := managedStore(t)
	definition := remoteDefinition()
	definition.Version = "9.9.9"
	definition.Tools[0].CapabilityID = "files.archive"
	definition.Tools[0].Uses = []api.CapabilityUse{{Action: "archive", Resource: "workspace", PathArgument: "path"}}
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(profile)
	var candidate api.CapabilityProfile
	if err := json.Unmarshal(body, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Revision++
	candidate.Selections = append(candidate.Selections, api.CapabilitySelection{CapabilityID: "files.archive",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: api.DefinitionDigest(definition),
		ToolName: "search", Name: "workspace_archive", ConnectionID: candidate.Selections[0].ConnectionID})
	if err := putProfile(t, s, candidate); err != nil {
		t.Fatalf("uncovered but never-governed selection must stay writable: %v", err)
	}
}

func TestPutCapabilityProfileRejectsDenyOnlySelection(t *testing.T) {
	s, _, _, profile := managedStore(t)
	// A deliberate deny must stay writable: a deny at the pinned digest does
	// not mark a repin conflict, so a profile that suppresses a still-ruled
	// selection is legal state.
	split := profile
	body, _ := json.Marshal(profile)
	if err := json.Unmarshal(body, &split); err != nil {
		t.Fatal(err)
	}
	split.Revision++
	selection := split.Selections[0]
	split.Denies = append(split.Denies, api.CapabilityRule{CapabilityID: selection.CapabilityID, ImplementationDigest: selection.ImplementationDigest,
		Action: "read", Resource: "workspace", ConnectionID: selection.ConnectionID})
	if err := putProfile(t, s, split); err != nil {
		t.Fatalf("deny-shrunk profile must stay writable: %v", err)
	}
}

func TestProfileReplayAndDisabledWritesTolerateDigestSplit(t *testing.T) {
	s, _, _, profile := managedStore(t)
	split := digestSplitProfile(t, s, profile)
	mustConfirm(t, &split)
	if err := s.putCapabilityProfile(split, true); err != nil {
		t.Fatalf("journal replay must tolerate a stored split: %v", err)
	}
	// Disabling the broken profile is the operator's unwedge path — a
	// disabled profile projects nothing, so the write is legal.
	split.Status = api.DisabledStatus
	split.Revision++
	mustConfirm(t, &split)
	if err := s.PutCapabilityProfile(split); err != nil {
		t.Fatalf("disabling a split profile must stay writable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("store holding a historical split must load: %v", err)
	}
	if got := loaded.capabilityProfiles["alice-default"]; got.ProfileID == "" {
		t.Fatal("replayed store lost the split profile record")
	}
}

func TestSelectionGapsReportsDigestSplit(t *testing.T) {
	s, _, _, profile := managedStore(t)
	if gaps, err := s.SelectionGaps(); err != nil || len(gaps) != 0 {
		t.Fatalf("consistent store must report no gaps: %+v %v", gaps, err)
	}
	split := digestSplitProfile(t, s, profile)
	mustConfirm(t, &split)
	if err := s.putCapabilityProfile(split, true); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.SelectionGaps()
	if err != nil || len(gaps["alice-default"]) != 1 || gaps["alice-default"][0] != "workspace_read" {
		t.Fatalf("digest-split selection must be reported: %+v %v", gaps, err)
	}
}

func TestCapabilityDenialsNamesRuleDeniedSelection(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	if denials, err := s.CapabilityDenials(auth); err != nil || len(denials) != 0 {
		t.Fatalf("consistent store must report no capability denials: %+v %v", denials, err)
	}
	definition := altDefinition()
	split := digestSplitProfile(t, s, profile)
	// The publish path also repins the binding: projection resolves the
	// selected revision, and only the rule check fails.
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "google-work",
		ConnectionRevision: 1, CredentialRefID: api.CredentialReferenceID("google-work", 1), CredentialRevision: 1,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, s, api.OperatorGrant(api.GrantDefinition, "alice", definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, &split)
	if err := s.putCapabilityProfile(split, true); err != nil {
		t.Fatal(err)
	}
	denials, err := s.CapabilityDenials(auth)
	if err != nil || len(denials) != 1 || denials[0].Name != "workspace_read" || denials[0].Reason != DenialCapabilityRules {
		t.Fatalf("rule-denied selection must surface with the capability_rules reason: %+v %v", denials, err)
	}
	if tools, err := s.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("denied selection must stay out of projection: %v %v", tools, err)
	}
}

// The same split a publish would leave — selections repinned past their
// rules — must answer tool_search truthfully: suppressed by capability
// rules, not "tool does not exist".
func TestToolsListReportsRuleDeniedSelection(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	definition := altDefinition()
	split := digestSplitProfile(t, s, profile)
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime",
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: "google-work",
		ConnectionRevision: 1, CredentialRefID: api.CredentialReferenceID("google-work", 1), CredentialRevision: 1,
		PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, s, api.OperatorGrant(api.GrantDefinition, "alice", definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, &split)
	if err := s.putCapabilityProfile(split, true); err != nil {
		t.Fatal(err)
	}
	grantToolUse(t, s, auth)

	g := &Gateway{Store: s}
	listed, err := g.toolsList(auth, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	unavailable, _ := listed["unavailable"].([]map[string]any)
	if len(unavailable) != 1 || unavailable[0]["name"] != "workspace_read" || unavailable[0]["reason"] != DenialCapabilityRules {
		t.Fatalf("inventory must name the rule-denied selection: %+v", listed)
	}
	exact, err := g.toolsList(auth, "", "workspace_read", 0)
	if err != nil {
		t.Fatal(err)
	}
	if exact["found"] != false || exact["reason"] != DenialCapabilityRules {
		t.Fatalf("exact lookup must report the denial reason: %+v", exact)
	}
}
