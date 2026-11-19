package identityhub

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestAgentProfilesNarrowTheirOwnerAndAllLayers(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	if _, err := r.ImportLegacy(ctx, "acme-import", "acme", []commodel.User{{ID: "alice", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	platform := contract.Restrictions{Grants: []string{"files.read", "files.write"}, Skills: []string{"reader", "writer"}, Guardrails: []string{"always-ask"}, MemoryScopes: []string{"user:alice", "org:acme"}, Mode: "full-access"}
	if err := r.SetLayer(ctx, "admin", "platform", platform); err != nil {
		t.Fatal(err)
	}
	if err := r.SetLayer(ctx, "admin", "org:acme", contract.Restrictions{Skills: []string{"reader"}, Guardrails: []string{"company-policy"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetLayer(ctx, "alice", "user:alice", contract.Restrictions{Grants: []string{"files.read"}, Guardrails: []string{"owner-policy"}, Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	agent, err := r.PutAgentProfile(ctx, "alice", "research", contract.Restrictions{MemoryScopes: []string{"org:acme"}, Guardrails: []string{"agent-policy"}, Mode: "read-only"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Template(ctx, contract.TemplateRequest{Subject: "alice", Actor: agent.ID, Space: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	want := contract.Restrictions{Grants: []string{"files.read"}, Skills: []string{"reader"}, Guardrails: []string{"agent-policy", "always-ask", "company-policy", "owner-policy"}, MemoryScopes: []string{"org:acme"}, Mode: "read-only"}
	if got.Subject.ID != "alice" || got.Actor.OwnerID != "alice" || got.SharedSpace != "acme" || !reflect.DeepEqual(got.Restrictions, want) {
		t.Fatalf("template: %+v", got)
	}
	if _, err := r.PutAgentProfile(ctx, "alice", "wider", contract.Restrictions{Skills: []string{"writer"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("profile widened org skills: %v", err)
	}
	if _, err := r.PutAgentProfile(ctx, "alice", "wider", contract.Restrictions{Mode: "full-access"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("profile widened owner mode: %v", err)
	}
	if err := r.SetLayer(ctx, "alice", "org:acme", contract.Restrictions{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("user edited org ceiling: %v", err)
	}
	if err := r.SetLayer(ctx, "alice", "user:alice", contract.Restrictions{Skills: []string{"writer"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("user widened org ceiling: %v", err)
	}
}

func TestEmptyLayerDeniesInsteadOfInheriting(t *testing.T) {
	r, mappings := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := context.Background()
	if err := r.SetLayer(ctx, "alice", "user:alice", contract.Restrictions{Grants: []string{}, Skills: []string{}, MemoryScopes: []string{}}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Template(ctx, contract.TemplateRequest{Subject: "alice", Actor: mappings[0].Agent, Space: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Restrictions.Grants == nil || got.Restrictions.Skills == nil || got.Restrictions.MemoryScopes == nil || len(got.Restrictions.Skills) != 0 {
		t.Fatalf("explicit deny lost: %+v", got.Restrictions)
	}
	if got.Restrictions.Mode != "ask-critical" {
		t.Fatalf("default mode: %s", got.Restrictions.Mode)
	}
	if _, err := r.PutAgentProfile(ctx, "alice", "wider", contract.Restrictions{Skills: []string{"reader"}}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("empty ceiling widened: %v", err)
	}
}

func TestTemplateDoesNotPermitForeignOrSystemActors(t *testing.T) {
	r, mappings := fixture(t, commodel.User{ID: "alice", Enabled: true}, commodel.User{ID: "bob", Enabled: true})
	ctx := context.Background()
	for _, query := range []contract.TemplateRequest{{Subject: "alice", Actor: mappings[1].Agent, Space: "alice"}, {Subject: mappings[0].Agent, Actor: mappings[0].Agent, Space: "alice"}, {Subject: "alice", Actor: mappings[0].Agent, Space: "bob"}, {Subject: "alice", Actor: "system:clock", Space: "alice"}} {
		if _, err := r.Template(ctx, query); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("foreign template %v: %v", query, err)
		}
	}
}

func TestCompanyBrainCannotAliasHumanSpace(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true})
	ctx := context.Background()
	if _, err := r.ImportLegacy(ctx, "foreign-org", "alice", []commodel.User{{ID: "bob", Enabled: true}}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("org shared space aliases private space: %v", err)
	}
	if _, err := r.ImportLegacy(ctx, "acme-import", "acme", []commodel.User{{ID: "carol", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ImportLegacy(ctx, "foreign-human", "personal", []commodel.User{{ID: "acme", Enabled: true}}); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("private space aliases company brain: %v", err)
	}
}

func TestCompanyBrainRequiresCurrentOrganizationMembership(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "outsider", Enabled: true})
	ctx := context.Background()
	if _, err := r.ImportLegacy(ctx, "company", "acme", []commodel.User{{ID: "alice", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	query := contract.TemplateRequest{Subject: "alice", Actor: "agent:alice:default", Space: "acme"}
	template, err := r.Template(ctx, query)
	if err != nil || template.SpaceKind != "organization" || template.OrganizationID != "acme" {
		t.Fatalf("company template: %+v %v", template, err)
	}
	if err := r.CanAct(ctx, "outsider", "agent:outsider:default", "acme"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("outsider entered company brain")
	}
	if _, err := r.db.Exec("DELETE FROM memberships WHERE principal_id='alice'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Template(ctx, query); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("retired member retained company brain")
	}
}

func TestTemplateNeedsCurrentOrganizationMembership(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	mappings, err := r.ImportLegacy(ctx, "acme-import", "acme", []commodel.User{{ID: "alice", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	query := contract.TemplateRequest{Subject: "alice", Actor: mappings[0].Agent, Space: "alice"}
	if _, err := r.Template(ctx, query); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, "DELETE FROM memberships WHERE principal_id='alice'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Template(ctx, query); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("removed org member retained company template: %v", err)
	}
	if err := r.CanAct(ctx, query.Subject, query.Actor, query.Space); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("removed org member retained acting authority: %v", err)
	}
}
