package identity_test

import (
	"context"
	"net/http"
	"testing"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
)

type fixture struct{}

func (fixture) Resolve(_ context.Context, channel identity.ChannelIdentity) (identity.Principal, error) {
	if channel == (identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}) {
		return identity.Principal{ID: "alice", Kind: identity.Human}, nil
	}
	return identity.Principal{}, identity.ErrNotFound
}

func (fixture) Principal(_ context.Context, id string) (identity.Principal, error) {
	if id == "alice" {
		return identity.Principal{ID: id, Kind: identity.Human}, nil
	}
	return identity.Principal{}, identity.ErrNotFound
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	server := fixture{}
	conformance.Both(t, server, func(token string) http.Handler { return identity.Handler(server, token) }, identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}, identity.Principal{ID: "alice", Kind: identity.Human})
}
