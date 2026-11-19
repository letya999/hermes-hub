package communication

import (
	"context"
	"errors"
	"net/http"
	"testing"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
)

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	g := &Gateway{config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}, SlackIDs: []SlackLink{{TeamID: "T1", UserID: "U1"}}}}}}
	server, ok := any(g).(identity.Client)
	if !ok {
		t.Fatal("gateway has no identity/v1 server")
	}
	for _, channel := range []identity.ChannelIdentity{{Channel: identity.Telegram, ID: "7"}, {Channel: identity.Slack, ID: "t1/u1"}} {
		conformance.Both(t, server, func(token string) http.Handler { return identity.Handler(server, token) }, channel, identity.Principal{ID: "alice", Kind: identity.Human})
	}
}

func TestLegacyIdentityMappingDeniesAmbiguityAndDisabled(t *testing.T) {
	g := &Gateway{config: Config{Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}, {ID: "alice2", Enabled: true, TelegramIDs: []int64{7}}, {ID: "off", TelegramIDs: []int64{8}}}}}
	server, ok := any(g).(identity.Client)
	if !ok {
		t.Fatal("gateway has no identity/v1 server")
	}
	if _, err := server.Resolve(context.Background(), identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("ambiguous link: %v", err)
	}
	if _, err := server.Principal(context.Background(), "off"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("disabled principal: %v", err)
	}
}
