package identityhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestSystemAdministrationRequiresExplicitScopedGrant(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true}, commodel.User{ID: "member", Enabled: true})
	ctx := context.Background()
	if err := r.AuthorizeAdministration(ctx, "admin", "platform", "principals.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("first user became administrator: %v", err)
	}
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	system, err := r.CreateSystem(ctx, "admin", "watchdog")
	if err != nil || system.ID != "system:watchdog" || system.Kind != contract.System || system.OwnerID != "" {
		t.Fatalf("system: %+v %v", system, err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("system inherited administrator: %v", err)
	}
	grant, err := r.GrantAdministration(ctx, "admin", system.ID, "platform", "layers.write", "operator-maintenance", time.Now().Add(time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "memberships.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("grant widened operation: %v", err)
	}
	if err := r.SetMembership(ctx, "admin", system.ID, "platform", "platform-admin"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("system acquired human role: %v", err)
	}
	if err := r.RevokeAdministration(ctx, "admin", grant); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("revoked grant survived: %v", err)
	}
}

func TestBreakGlassIsScopedExpiringAndAudited(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true}, commodel.User{ID: "member", Enabled: true})
	ctx := context.Background()
	now := time.Now()
	r.now = func() time.Time { return now }
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GrantAdministration(ctx, "member", "member", "platform", "principals.write", "incident-response", now.Add(time.Minute), true); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("self-issued break glass: %v", err)
	}
	if _, err := r.GrantAdministration(ctx, "admin", "member", "platform", "principals.write", "incident-response", now.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, "member", "platform", "principals.write"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM audit WHERE action='break_glass_use' AND actor='member' AND reason='incident-response'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("break glass audit count=%d err=%v", count, err)
	}
	if err := r.AuthorizeAdministration(ctx, "member", "platform", "memberships.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("implicit super-admin bypass: %v", err)
	}
	now = now.Add(time.Minute)
	if err := r.AuthorizeAdministration(ctx, "member", "platform", "principals.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("expired break glass: %v", err)
	}
}

func TestIdentityDiagnosticUsesOnlySafeCounts(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{7}, ConfiguredEnv: map[string]bool{"credential-key-marker": true}})
	ctx := context.Background()
	challenge, err := r.BeginLink(ctx, "alice", contract.ChannelIdentity{Channel: contract.Telegram, ID: "8"}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := r.Diagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":2,"humans":1,"agents":1,"systems":0,"organizations":0,"verified_links":1,"pending_links":1,"audit_events":2}`
	if string(raw) != want {
		t.Fatalf("diagnostic schema: %s", raw)
	}
	for _, marker := range []string{"alice", "credential-key-marker", challenge.ID, challenge.Proof} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("diagnostic exposed identity or credential material")
		}
	}
}

func TestOrganizationRolesCannotCrossTheirScope(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	if _, err := r.ImportLegacy(ctx, "acme-import", "acme", []commodel.User{{ID: "orgadmin", Enabled: true}, {ID: "member", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ImportLegacy(ctx, "other-import", "other", []commodel.User{{ID: "outsider", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, "orgadmin", "org:acme", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("member inherited administration: %v", err)
	}
	if err := r.SetMembership(ctx, "admin", "orgadmin", "org:acme", "org-admin"); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, "orgadmin", "org:acme", "layers.write"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"org:other", "platform"} {
		if err := r.AuthorizeAdministration(ctx, "orgadmin", scope, "layers.write"); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("foreign scope %s: %v", scope, err)
		}
		role := "org-admin"
		if scope == "platform" {
			role = "platform-admin"
		}
		if err := r.SetMembership(ctx, "orgadmin", "member", scope, role); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("cross-scope promotion: %v", err)
		}
	}
	if err := r.AuthorizeAdministration(ctx, "agent:orgadmin:default", "org:acme", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("agent inherited owner role: %v", err)
	}
}

func TestAuditFailureRollsBackAdministrativeGrant(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	system, err := r.CreateSystem(ctx, "admin", "clock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, "CREATE TRIGGER deny_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GrantAdministration(ctx, "admin", system.ID, "platform", "layers.write", "operator-maintenance", time.Now().Add(time.Minute), false); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatalf("grant survived failed audit: %v", err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("unaudited authority exists: %v", err)
	}
}

func TestDelegatedAdministrationDiesWithIssuerRights(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true})
	ctx := context.Background()
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	system, err := r.CreateSystem(ctx, "admin", "clock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GrantAdministration(ctx, "admin", system.ID, "platform", "layers.write", "operator-maintenance", time.Now().Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, "DELETE FROM memberships WHERE principal_id='admin'"); err != nil {
		t.Fatal(err)
	}
	if err := r.AuthorizeAdministration(ctx, system.ID, "platform", "layers.write"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("delegation survived issuer revocation: %v", err)
	}
}

func TestAdminGrantTerminalStatesRefuseTransitions(t *testing.T) {
	r, _ := fixture(t, commodel.User{ID: "admin", Enabled: true}, commodel.User{ID: "member", Enabled: true})
	ctx := context.Background()
	now := time.Now()
	r.now = func() time.Time { return now }
	if err := r.BootstrapPlatformAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	grant, err := r.GrantAdministration(ctx, "admin", "member", "platform", "layers.write", "account-recovery", now.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := r.RevokeAdministration(ctx, "admin", grant); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("expired grant changed state: %v", err)
	}
	grant, err = r.GrantAdministration(ctx, "admin", "member", "platform", "layers.write", "account-recovery", now.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RevokeAdministration(ctx, "admin", grant); err != nil {
		t.Fatal(err)
	}
	if err := r.RevokeAdministration(ctx, "admin", grant); !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("revoked grant changed state: %v", err)
	}
}
