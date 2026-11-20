package fake_test

import (
	"net/http"
	"testing"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
	"github.com/letya999/hermes-hub/contracts/identity/v1/fake"
)

func TestFakePassesConformance(t *testing.T) {
	known := identity.Principal{ID: "alice", Kind: identity.Human, OrganizationID: "org-1"}
	server := fake.Server{
		Principals: map[string]identity.Principal{known.ID: known},
		Links:      []identity.ChannelBinding{{ID: "link-1", PrincipalID: known.ID, Channel: identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}}},
	}
	conformance.Both(t, server, func(token string) http.Handler { return identity.Handler(server, token) }, identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}, known)
}
