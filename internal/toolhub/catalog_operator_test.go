package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShippedCLITrustRequiresExactReleaseBytes(t *testing.T) {
	for _, d := range api.CLICatalogDefinitions() {
		attrs := projectedToolAttrs(api.EffectiveBinding{Definition: d}, d.Tools[0])
		if attrs["owner"] != api.OwnerHub || attrs["official"] != "true" {
			t.Fatalf("shipped catalog not trusted: %v", attrs)
		}
		g := api.NewGovernance()
		if g.Evaluate("alice", "", api.SectionToolHub, attrs, time.Now()).Effect != api.RuleAllow {
			t.Fatal("shipped CLI still needs transport-wide workaround")
		}
		d.Source.Args = []string{"--malicious"}
		attrs = projectedToolAttrs(api.EffectiveBinding{Definition: d}, d.Tools[0])
		if attrs["owner"] != api.OwnerExternal || attrs["official"] != "false" || g.Evaluate("alice", "", api.SectionToolHub, attrs, time.Now()).Effect != api.RuleDeny {
			t.Fatalf("id/transport spoof trusted: %v", attrs)
		}
	}
}

func TestCatalogEnableManagedAtomicAuthorityAndIsolation(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	d := api.CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteToCatalog(d.DefinitionID, d.Version, "operator"); err != nil {
		t.Fatal(err)
	}
	sibling := profile
	sibling.ProfileID, sibling.PrincipalID, sibling.ContextID = "bob-default", "bob", "bob"
	sibling.Selections, sibling.Allows = nil, nil
	if err := putProfile(t, s, sibling); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "store.json")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	rightsDoc, rightsLedger := s.RightsDoc, s.RightsLedger
	var loadErr error
	s, loadErr = Load(p)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	s.RightsDoc, s.RightsLedger = rightsDoc, rightsLedger
	before, _ := os.ReadFile(p)
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("implicit ceiling widening: %v", err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) || s.capabilityPolicies[policy.PolicyID].Revision != policy.Revision {
		t.Fatal("rejected catalog mutation leaked authority")
	}
	foreign := auth
	foreign.PrincipalID = "bob"
	if _, err := s.EnableCatalog(foreign, d.DefinitionID, d.Version, "operator", true); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("foreign profile accepted: %v", err)
	}
	binding, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	loaded.RightsDoc, loaded.RightsLedger = rightsDoc, rightsLedger
	tools, err := loaded.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProjectedBinding(tools, binding.ToolBindingID) {
		t.Fatalf("catalog selection not projected: %+v", tools)
	}
	bob := auth
	bob.PrincipalID, bob.ContextID, bob.CapabilityProfile = "bob", "bob", "bob-default"
	bobTools, err := loaded.ListProjectedTools(bob)
	if err != nil {
		t.Fatal(err)
	}
	if containsProjectedBinding(bobTools, binding.ToolBindingID) {
		t.Fatal("catalog binding leaked to bob")
	}
	bobProfile := loaded.capabilityProfiles["bob-default"]
	if bobProfile.PolicyRevision != policy.Revision+1 || len(bobProfile.Allows) != 0 || len(bobProfile.Selections) != 0 {
		t.Fatalf("sibling repin widened access: %+v", bobProfile)
	}
	if _, err := loaded.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(loaded.capabilityProfiles[auth.CapabilityProfile].Selections) != len(profile.Selections)+1 {
		t.Fatal("duplicate catalog selection")
	}
	// A real catalog update must lose the disk fence for a stale writer.
	second := d
	second.Version = "2.0.0"
	if err := loaded.RegisterDefinition(second); err != nil {
		t.Fatal(err)
	}
	if err := loaded.PromoteToCatalog(second.DefinitionID, second.Version, "operator"); err != nil {
		t.Fatal(err)
	}
	stale, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.EnableCatalog(auth, second.DefinitionID, second.Version, "operator", true); err != nil {
		t.Fatal(err)
	}
	revision := stale.capabilityProfiles[auth.CapabilityProfile].Revision
	if _, err := stale.EnableCatalog(auth, second.DefinitionID, second.Version, "operator", true); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("stale writer accepted: %v", err)
	}
	if stale.capabilityProfiles[auth.CapabilityProfile].Revision != revision {
		t.Fatal("failed persistence leaked in-memory revision")
	}
}

func TestCatalogEnableSelectsCanonicalToolNames(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	d := api.CLICatalogDefinitions()[1]
	d.Tools = append(d.Tools, api.ToolSpec{Name: "session_list", CapabilityID: "sessions.list", Requires: []string{"sessions.list"}, Effect: api.ReadEffect, Audience: api.AudienceModel, Executor: "runtime:sessions"})
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true); err != nil {
		t.Fatal(err)
	}
	profile := s.capabilityProfiles[auth.CapabilityProfile]
	seen := map[string]bool{}
	for _, selection := range profile.Selections {
		if selection.DefinitionID != d.DefinitionID || selection.DefinitionVersion != d.Version {
			continue
		}
		if selection.Name != selection.ToolName {
			t.Fatalf("catalog selection projected under generated name %q for tool %q", selection.Name, selection.ToolName)
		}
		seen[selection.Name] = true
	}
	for _, tool := range d.Tools {
		if !seen[tool.Name] {
			t.Fatalf("catalog tool %q missing from selections", tool.Name)
		}
	}
}

func TestCatalogEnableReplacesVersionWithoutWideningPathScope(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	old := api.CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, old.DefinitionID, old.Version, "operator", true); err != nil {
		t.Fatal(err)
	}
	profile := s.capabilityProfiles[auth.CapabilityProfile]
	for i := range profile.Selections {
		if profile.Selections[i].DefinitionID == old.DefinitionID {
			profile.Selections[i].PathScopes = []string{"/workspace/work"}
		}
	}
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Version = "9.9.9"
	next.Tools[0].Description += " Updated version."
	if err := s.RegisterDefinition(next); err != nil {
		t.Fatal(err)
	}
	binding, err := s.EnableCatalog(auth, next.DefinitionID, next.Version, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	updated := s.capabilityProfiles[auth.CapabilityProfile]
	count := 0
	for _, selection := range updated.Selections {
		if selection.DefinitionID != next.DefinitionID {
			continue
		}
		count++
		if selection.DefinitionVersion != next.Version || len(selection.PathScopes) != 1 || selection.PathScopes[0] != "/workspace/work" {
			t.Fatalf("catalog upgrade widened or retained stale selection: %+v", selection)
		}
	}
	if count != 1 || updated.Revision <= profile.Revision || updated.PolicyRevision <= profile.PolicyRevision {
		t.Fatalf("catalog upgrade revisions/selections: count=%d profile=%d policy=%d", count, updated.Revision, updated.PolicyRevision)
	}
	if binding.Status != api.ActiveStatus || updated.Validate() != nil {
		t.Fatal("upgraded binding or profile invalid")
	}
}

func containsProjectedBinding(tools []api.ProjectedTool, id string) bool {
	for _, tool := range tools {
		if tool.BindingID == id {
			return true
		}
	}
	return false
}

func TestCatalogEnableRejectsMissingManagedContractAtomically(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	d := api.CLICatalogDefinitions()[1]
	d.Version = "9.0.0"
	d.Tools[0].CapabilityID = ""
	d.Tools[0].Uses = nil
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("uncontracted tool admitted: %v", err)
	}
	if s.capabilityProfiles[profile.ProfileID].Revision != profile.Revision {
		t.Fatal("rejected contract changed profile")
	}
	for _, binding := range s.bindings {
		if binding.DefinitionID == d.DefinitionID {
			t.Fatal("rejected contract left a binding")
		}
	}
}

func TestCatalogEnablePreservesExplicitManagedDeny(t *testing.T) {
	s, auth, _, _ := managedStore(t)
	d := api.CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	binding, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	profile := s.capabilityProfiles[auth.CapabilityProfile]
	for _, rule := range profile.Allows {
		if rule.ImplementationDigest == api.DefinitionDigest(d) {
			rule.Limits = api.CapabilityLimits{}
			profile.Denies = append(profile.Denies, rule)
		}
	}
	profile.Revision++
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	if containsProjectedBinding(tools, binding.ToolBindingID) {
		t.Fatal("catalog enable bypassed explicit managed deny")
	}
	if len(s.capabilityProfiles[auth.CapabilityProfile].Denies) != len(profile.Denies) {
		t.Fatal("catalog enable erased denies")
	}
}

func TestCatalogEnableConcurrentAndDenials(t *testing.T) {
	s := NewStore()
	d := api.CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.bindings) != 1 {
		t.Fatalf("duplicate bindings: %d", len(s.bindings))
	}
	for id, binding := range s.bindings {
		for _, status := range []api.Status{api.DisabledStatus, api.RevokedStatus} {
			binding.Status = status
			s.bindings[id] = binding
			if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, api.ErrConflict) {
				t.Fatalf("catalog resurrected %s binding: %v", status, err)
			}
		}
		binding.Status = api.ActiveStatus
		binding.PolicyVersion = "stale-policy"
		s.bindings[id] = binding
		if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, api.ErrConflict) {
			t.Fatal("catalog silently replaced stale authority")
		}
	}
	for _, issuer := range []string{"model", "hermes", ""} {
		if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, issuer, false); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if _, err := s.EnableCatalog(auth, "missing", d.Version, "operator", false); !errors.Is(err, api.ErrNotFound) {
		t.Fatal(err)
	}
	bad := auth
	bad.PrincipalID = "../bob"
	if _, err := s.EnableCatalog(bad, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, api.ErrInvalid) {
		t.Fatal(err)
	}
	d.Version = "9.0.0"
	d.Credentials = []api.CredentialInput{{Name: "ACCESS_TOKEN", Required: true}}
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestCLIReleaseWrongFieldHasActionableError(t *testing.T) {
	fix := controlCLIFixture(t, true)
	_, err := fix.control.prepareSource(context.Background(), aliceAuth(), map[string]any{"source": "github-release:jqlang/jq@latest"})
	if !errors.Is(err, api.ErrInvalid) || !strings.Contains(err.Error(), "cli.source") {
		t.Fatalf("wrong-field hint: %v", err)
	}
	_, schema := controlToolContract("prepare_source")
	cli := schema["properties"].(map[string]any)["cli"].(map[string]any)
	if cli["additionalProperties"] != false || len(cli["oneOf"].([]any)) != 2 {
		t.Fatalf("unstructured CLI schema: %v", cli)
	}
	for _, raw := range cli["examples"].([]any) {
		spec := raw.(map[string]any)
		if _, err := cliSpecTools(spec["tools"]); err != nil {
			t.Fatalf("example cannot compile: %v", err)
		}
		if _, err := api.ParseGitHubReleaseSource(api.ArgString(spec, "source"), api.ArgString(spec, "asset"), "", ""); err != nil {
			t.Fatal(err)
		}
	}
}
