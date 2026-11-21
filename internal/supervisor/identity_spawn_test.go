package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestIdentityTemplateDriftRefusesMaterialization(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	b := binding(root)
	_, revision, err := m.identityTemplate(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	b.IdentityRevision = revision
	r := m.cfg.Identity.(*identityhub.Registry)
	if err := r.SetLayer(context.Background(), "alice", "user:alice", contract.Restrictions{Mode: "read-only", Guardrails: []string{"owner-policy"}, MemoryScopes: []string{}}); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEffective(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.applyIdentityTemplate(b, settings); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("mixed identity snapshot accepted: %v", err)
	}
	b.IdentityRevision = ""
	settings, err = m.applyIdentityTemplate(b, settings)
	if err != nil || settings.IdentityRestrictions.Mode != "read-only" || settings.IdentityRestrictions.MemoryScopes == nil || len(settings.IdentityRestrictions.Guardrails) != 1 {
		t.Fatal("spawn lost narrowing template")
	}
	b.SubjectID = "bob"
	if _, _, err := m.identityTemplate(context.Background(), b); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("private runtime accepted another subject")
	}
}

func TestParallelAgentProfilesKeepSeparateSkillViews(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	for _, name := range []string{"reader", "writer"} {
		dir := filepath.Join(root, "skills", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Fixture skill\n---\nFixture.\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := stack.ReadEffective(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultGlobalSkillsDir(filepath.Join(filepath.Dir(filepath.Dir(root)), "config", "skills"))
	if err := os.MkdirAll(settings.GlobalSkillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	idx, err := stack.MaterializeEffectiveSkills(root, settings)
	if err != nil {
		t.Fatal(err)
	}
	r := m.cfg.Identity.(*identityhub.Registry)
	bindings := make([]Binding, 2)
	for i, name := range []string{"reader", "writer"} {
		var allowed []string
		for _, entry := range idx.Skills {
			if entry.Name != []string{"writer", "reader"}[i] {
				allowed = append(allowed, entry.Name)
			}
		}
		profile, err := r.PutAgentProfile(context.Background(), "alice", name, contract.Restrictions{Skills: allowed})
		if err != nil {
			t.Fatal(err)
		}
		b := binding(root)
		b.RuntimeMode = "gateway-" + name
		b.AgentID = profile.ID
		bindings[i] = b
	}
	stub := m.cfg.Spawn.(*claimStub)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, b := range bindings {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.Ensure(context.Background(), b); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Skill-view materialization itself moved spawn-side with the claim;
	// the supervisor-side guarantee left here is that each profile's claim
	// carries exactly its own restriction set — never the other's.
	claims := stub.claimed()
	if len(claims) != 2 {
		t.Fatalf("claims=%d", len(claims))
	}
	byMode := map[string][]string{}
	for _, in := range claims {
		if in.Restrictions == nil {
			t.Fatal("claim carried no restrictions")
		}
		byMode[in.RuntimeMode] = in.Restrictions.Skills
	}
	for i, b := range bindings {
		allowed, denied := []string{"reader", "writer"}[i], []string{"writer", "reader"}[i]
		got := byMode[b.RuntimeMode]
		var hasAllowed, hasDenied bool
		for _, skill := range got {
			hasAllowed = hasAllowed || skill == allowed
			hasDenied = hasDenied || skill == denied
		}
		if !hasAllowed || hasDenied {
			t.Fatalf("profile %s claimed skills %v, want %s allowed and %s absent", b.RuntimeMode, got, allowed, denied)
		}
	}
}
