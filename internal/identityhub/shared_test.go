package identityhub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestLegacyGroupSpacePreservesMembersWithoutInventedOwner(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	input := LegacyConfiguration{Name: "shared", Organization: "acme", Users: []commodel.User{
		{ID: "alice", Enabled: true, TelegramIDs: []int64{7}},
		{ID: "bob", Enabled: true, TelegramIDs: []int64{8}},
		{ID: "team", Enabled: true},
	}, SharedSpaces: []LegacySharedSpace{{ID: "team", Members: []int64{7, 99}}}}
	mappings, err := r.ImportLegacyConfiguration(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 3 {
		t.Fatalf("resource mapping count: %d", len(mappings))
	}
	if _, err := r.Principal(ctx, "team"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("group became a human")
	}
	if err := r.CanAct(ctx, "alice", "agent:alice:default", "team"); err != nil {
		t.Fatalf("existing member denied: %v", err)
	}
	if err := r.CanAct(ctx, "bob", "agent:bob:default", "team"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("non-member admitted")
	}
	guest, err := r.Resolve(ctx, contract.ChannelIdentity{Channel: contract.Telegram, ID: "99"})
	if err != nil || guest.Kind != contract.Human {
		t.Fatalf("explicit guest lost access: %+v %v", guest, err)
	}
	query := contract.TemplateRequest{Subject: guest.ID, Actor: "agent:" + guest.ID + ":default", Space: "team"}
	template, err := r.Template(ctx, query)
	if err != nil || template.Space != "team" || template.SharedSpace != "" {
		t.Fatalf("guest authority: %+v %v", template, err)
	}
	for _, space := range []string{"alice", "bob", "acme", guest.ID} {
		if err := r.CanAct(ctx, guest.ID, query.Actor, space); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("guest gained space %s: %v", space, err)
		}
	}
}

func TestLegacyOpenGroupFreezesExistingTelegramMembership(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	_, err = r.ImportLegacyConfiguration(ctx, LegacyConfiguration{Name: "group", Organization: "personal", Users: []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, {ID: "slackonly", Enabled: true}, {ID: "team", Enabled: true}}, SharedSpaces: []LegacySharedSpace{{ID: "team"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CanAct(ctx, "alice", "agent:alice:default", "team"); err != nil {
		t.Fatal(err)
	}
	if err := r.CanAct(ctx, "slackonly", "agent:slackonly:default", "team"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("empty legacy group widened beyond Telegram users")
	}
}

func TestSharedMembershipChangeNeedsAdministratorAndRevokesImmediately(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	_, err = r.ImportLegacyConfiguration(ctx, LegacyConfiguration{Name: "group", Organization: "personal", Users: []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, {ID: "bob", Enabled: true, TelegramIDs: []int64{8}}, {ID: "team", Enabled: true}}, SharedSpaces: []LegacySharedSpace{{ID: "team", Members: []int64{7}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetSharedMembers(ctx, "alice", "team", []string{"bob"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("ordinary member changed shared ACL")
	}
	if err := r.BootstrapPlatformAdmin(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSharedMembers(ctx, "agent:alice:default", "team", []string{"bob"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("agent inherited owner administration")
	}
	if err := r.SetSharedMembers(ctx, "alice", "team", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CanAct(ctx, "alice", "agent:alice:default", "team"); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("old member retained access")
	}
	if err := r.CanAct(ctx, "bob", "agent:bob:default", "team"); err != nil {
		t.Fatal("new member denied")
	}
	if err := r.SetSharedMembers(ctx, "alice", "team", []string{"unknown"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("unknown member added")
	}
	if err := r.CanAct(ctx, "bob", "agent:bob:default", "team"); err != nil {
		t.Fatal("failed ACL change lost old members")
	}
}

func TestLegacyGroupCannotResurrectDisabledHumanAsGuest(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	_, err = r.ImportLegacyConfiguration(ctx, LegacyConfiguration{Name: "group", Organization: "personal", Users: []commodel.User{{ID: "alice", Enabled: false, TelegramIDs: []int64{7}}, {ID: "team", Enabled: true}}, SharedSpaces: []LegacySharedSpace{{ID: "team", Members: []int64{7}}}})
	if !errors.Is(err, contract.ErrConflict) {
		t.Fatalf("disabled identity resurrected as guest: %v", err)
	}
	if _, err := r.Principal(ctx, "guest-telegram-7"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatal("partial guest survived refused import")
	}
}
