package communication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

func TestChannelLookupUsesRegistryAuthority(t *testing.T) {
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	users := []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}
	if _, err := r.ImportLegacy(context.Background(), "fixture", "personal", users); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(contract.Handler(r, "identity-token"))
	defer server.Close()
	g := &Gateway{config: Config{Users: users, OrganizationID: "personal", Identity: contract.NewHTTPClient(server.URL, "identity-token", server.Client())}}
	got, ok := g.identityUser(context.Background(), contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"})
	if !ok || got.ID != "alice" {
		t.Fatalf("registry route: user=%s ok=%v", got.ID, ok)
	}
	// A new channel link is authoritative even when the legacy list lacks it.
	users[0].TelegramIDs = nil
	got, ok = g.identityUser(context.Background(), contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"})
	if !ok || got.ID != "alice" {
		t.Fatal("route still depends on legacy channel list")
	}
}

func TestChannelLookupNeverFallsBackToLegacyConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	users := []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}
	for _, client := range []contract.Client{nil, contract.NewHTTPClient(server.URL, "identity-token", server.Client())} {
		g := &Gateway{config: Config{Users: users, Identity: client, Groups: Groups{{ChatID: -100, Space: "team", Members: []int64{7}}}}}
		if user, ok := g.identityUser(context.Background(), contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}); ok || user.ID != "" {
			t.Fatal("legacy mapping bypassed unavailable identity authority")
		}
		if _, ok := g.telegramUserFor(7, 7); ok {
			t.Fatal("Telegram ingress used legacy cache")
		}
		if user := g.userByChatID(7); user.ID != "" {
			t.Fatal("reverse chat lookup used legacy cache")
		}
		if _, _, ok := g.ownerHome(users[0]); ok {
			t.Fatal("owner notice used legacy cache")
		}
		if g.groupMember(g.config.Groups[0], 7) {
			t.Fatal("group ACL bypassed unavailable identity authority")
		}
		if err := g.ingestSlackEvent(context.Background(), "t1", "event", "message", "", "u1", "hello", "D123", "im", "", "", "", ""); err == nil {
			t.Fatal("Slack ingress bypassed unavailable identity authority")
		}
	}
}

func TestRevokedChannelCannotReceiveOwnerNoticesOrEnterGateway(t *testing.T) {
	c := baseTestConfig(t)
	g, err := newTestGateway(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := g.ownerHome(c.Users[0]); !ok {
		t.Fatal("live owner has no home")
	}
	fixtureRevokeChannel(t, g, 11)
	if _, ok := g.telegramUserFor(11, 11); ok {
		t.Fatal("revoked account entered gateway")
	}
	if _, _, ok := g.ownerHome(c.Users[0]); ok {
		t.Fatal("revoked account retained owner notices")
	}
}
