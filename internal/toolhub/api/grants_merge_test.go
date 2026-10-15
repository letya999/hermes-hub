package api

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGrantValidateContract(t *testing.T) {
	grant := OperatorGrant(GrantDefinition, "principal-1", "def-1", "1.0.0")
	grant.Confirmation = &Confirmation{Digest: ConfirmationDigest(grant), IssuedBy: "operator", ConfirmedAt: time.Now()}
	if err := grant.Validate(); err != nil {
		t.Fatalf("operator definition grant: %v", err)
	}
	control := OperatorControlGrant("principal-1", "enable")
	control.Confirmation = &Confirmation{Digest: ConfirmationDigest(control), IssuedBy: "operator", ConfirmedAt: time.Now()}
	if err := control.Validate(); err != nil || !ValidControlOperation("invoke") || ValidControlOperation("drop") {
		t.Fatalf("control grant: %v", err)
	}
	// model or hermes can never issue
	bad := grant
	bad.IssuedBy = "model"
	if err := bad.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("model grant: %v", err)
	}
	for name, mutate := range map[string]func(*Grant){
		"schema":     func(g *Grant) { g.Schema = 99 },
		"principal":  func(g *Grant) { g.PrincipalID = "" },
		"revision":   func(g *Grant) { g.Revision = 0 },
		"status":     func(g *Grant) { g.Status = "pending" },
		"kind":       func(g *Grant) { g.Kind = "everything" },
		"op on def":  func(g *Grant) { g.Operation = "enable" },
		"bad defver": func(g *Grant) { g.DefinitionVersion = "nope" },
	} {
		g := grant
		mutate(&g)
		if err := g.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// catalog/self-install grants cannot name a definition
	cat := OperatorGrant(GrantCatalogDefault, "p1", "", "")
	cat.DefinitionID = "def-1"
	if err := cat.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("catalog grant with definition: %v", err)
	}
	// control grant naming a definition or unknown operation
	op := control
	op.DefinitionID = "def-1"
	if err := op.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control grant with definition: %v", err)
	}
	op = control
	op.Operation = "shell"
	if err := op.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown operation: %v", err)
	}
}

func TestOnboardingAndPolicyValidation(t *testing.T) {
	onboarding := Onboarding{
		Schema: SchemaVersion, OnboardingID: "onb-1", PrincipalID: "p1", ContextID: "ctx-1",
		RuntimeID: "rt-1", PolicyVersion: "pol-1", Mode: OnboardingCatalog, Phase: PhaseReview,
		DefinitionID: "def-1", DefinitionVersion: "1.0.0", Revision: 1, CreatedAt: time.Now(),
	}
	if err := onboarding.Validate(); err != nil {
		t.Fatalf("onboarding: %v", err)
	}
	for name, mutate := range map[string]func(*Onboarding){
		"identity": func(o *Onboarding) { o.RuntimeID = "" },
		"mode":     func(o *Onboarding) { o.Mode = "yolo" },
		"phase":    func(o *Onboarding) { o.Phase = "halfway" },
		"defid":    func(o *Onboarding) { o.DefinitionID = " " },
		"defver":   func(o *Onboarding) { o.DefinitionVersion = "x" },
	} {
		o := onboarding
		mutate(&o)
		if err := o.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	for _, mode := range []string{OnboardingSelfInstall, OnboardingRemote} {
		o := onboarding
		o.Mode = mode
		if err := o.Validate(); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}

	pub := DefinitionPublication{DefinitionID: "def-1", Version: "1.0.0", Visibility: PublicationUser, OwnerPrincipalID: "p1"}
	if err := pub.Validate(); err != nil {
		t.Fatalf("user publication: %v", err)
	}
	pub.OwnerPrincipalID = ""
	if err := pub.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ownerless user publication")
	}
	pub.Visibility = PublicationCatalog
	if err := pub.Validate(); err != nil {
		t.Fatalf("catalog publication: %v", err)
	}
	pub.Visibility = "friends"
	if err := pub.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown visibility")
	}

	policy := SharedCredentialPolicy{
		Schema: SchemaVersion, PolicyID: "pol-1", DefinitionID: "def-1", StoreOwner: "owner-1",
		Locator: "vault://entry", Principals: []string{"p1", "p2"}, IssuedBy: "operator", Status: ActiveStatus,
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy: %v", err)
	}
	p2 := policy
	p2.IssuedBy = "hermes"
	if err := p2.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("hermes-issued policy")
	}
	for name, mutate := range map[string]func(*SharedCredentialPolicy){
		"locator":    func(p *SharedCredentialPolicy) { p.Locator = "a=b" },
		"principals": func(p *SharedCredentialPolicy) { p.Principals = nil },
		"dup":        func(p *SharedCredentialPolicy) { p.Principals = []string{"p1", "p1"} },
		"status":     func(p *SharedCredentialPolicy) { p.Status = "pending" },
	} {
		p := policy
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	p3 := policy
	p3.Locator = strings.Repeat("x", 257)
	if err := p3.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized locator")
	}
}
