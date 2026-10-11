package toolhub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShippedCLITrustRequiresExactReleaseBytes(t *testing.T) {
	for _, d := range CLICatalogDefinitions() {
		attrs := projectedToolAttrs(EffectiveBinding{Definition: d}, d.Tools[0])
		if attrs["owner"] != OwnerHub || attrs["official"] != "true" {
			t.Fatalf("shipped catalog not trusted: %v", attrs)
		}
		g := NewGovernance()
		if g.Evaluate("alice", "", SectionToolHub, attrs, time.Now()).Effect != RuleAllow {
			t.Fatal("shipped CLI still needs transport-wide workaround")
		}
		d.Source.Args = []string{"--malicious"}
		attrs = projectedToolAttrs(EffectiveBinding{Definition: d}, d.Tools[0])
		if attrs["owner"] != OwnerExternal || attrs["official"] != "false" || g.Evaluate("alice", "", SectionToolHub, attrs, time.Now()).Effect != RuleDeny {
			t.Fatalf("id/transport spoof trusted: %v", attrs)
		}
	}
}

func TestCatalogEnableManagedAtomicAuthorityAndIsolation(t *testing.T) {
	s, auth, policy, profile := managedStore(t)
	d := CLICatalogDefinitions()[1]
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
	var loadErr error
	s, loadErr = Load(p)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	before, _ := os.ReadFile(p)
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("implicit ceiling widening: %v", err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) || s.capabilityPolicies[policy.PolicyID].Revision != policy.Revision {
		t.Fatal("rejected catalog mutation leaked authority")
	}
	foreign := auth
	foreign.PrincipalID = "bob"
	if _, err := s.EnableCatalog(foreign, d.DefinitionID, d.Version, "operator", true); !errors.Is(err, ErrUnauthorized) {
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
	// A second writer must lose the disk fence without changing its memory.
	stale, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); err != nil {
		t.Fatal(err)
	}
	revision := stale.capabilityProfiles[auth.CapabilityProfile].Revision
	if _, err := stale.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale writer accepted: %v", err)
	}
	if stale.capabilityProfiles[auth.CapabilityProfile].Revision != revision {
		t.Fatal("failed persistence leaked in-memory revision")
	}
}

func containsProjectedBinding(tools []ProjectedTool, id string) bool {
	for _, tool := range tools {
		if tool.BindingID == id {
			return true
		}
	}
	return false
}

func TestCatalogEnableRejectsMissingManagedContractAtomically(t *testing.T) {
	s, auth, _, profile := managedStore(t)
	d := CLICatalogDefinitions()[1]
	d.Version = "9.0.0"
	d.Tools[0].CapabilityID = ""
	d.Tools[0].Uses = nil
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true); !errors.Is(err, ErrInvalid) {
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
	d := CLICatalogDefinitions()[1]
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	binding, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	profile := s.capabilityProfiles[auth.CapabilityProfile]
	for _, rule := range profile.Allows {
		if rule.ImplementationDigest == DefinitionDigest(d) {
			rule.Limits = CapabilityLimits{}
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
	d := CLICatalogDefinitions()[1]
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
		for _, status := range []Status{DisabledStatus, RevokedStatus} {
			binding.Status = status
			s.bindings[id] = binding
			if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrConflict) {
				t.Fatalf("catalog resurrected %s binding: %v", status, err)
			}
		}
		binding.Status = ActiveStatus
		binding.PolicyVersion = "stale-policy"
		s.bindings[id] = binding
		if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrConflict) {
			t.Fatal("catalog silently replaced stale authority")
		}
	}
	for _, issuer := range []string{"model", "hermes", ""} {
		if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, issuer, false); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if _, err := s.EnableCatalog(auth, "missing", d.Version, "operator", false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	bad := auth
	bad.PrincipalID = "../bob"
	if _, err := s.EnableCatalog(bad, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	d.Version = "9.0.0"
	d.Credentials = []CredentialInput{{Name: "ACCESS_TOKEN", Required: true}}
	if err := s.RegisterDefinition(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnableCatalog(auth, d.DefinitionID, d.Version, "operator", false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestCLIReleaseWrongFieldHasActionableError(t *testing.T) {
	fix := controlCLIFixture(t, true)
	_, err := fix.control.prepareSource(context.Background(), aliceAuth(), map[string]any{"source": "github-release:jqlang/jq@latest"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cli.source") {
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
		if _, err := parseGitHubReleaseSource(argString(spec, "source"), argString(spec, "asset"), "", ""); err != nil {
			t.Fatal(err)
		}
	}
}
