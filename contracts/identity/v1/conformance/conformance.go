// Package conformance checks identity/v1 transports and their real owners.
package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
)

func Run(t *testing.T, client identity.Client, channel identity.ChannelIdentity, known identity.Principal) {
	t.Helper()
	ctx := context.Background()
	got, err := client.Resolve(ctx, channel)
	if err != nil || got != known {
		t.Fatalf("resolve: got %+v, %v; want %+v", got, err, known)
	}
	got, err = client.Principal(ctx, known.ID)
	if err != nil || got != known {
		t.Fatalf("principal: got %+v, %v; want %+v", got, err, known)
	}
	if _, err := client.Principal(ctx, known.ID+"-missing"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("missing principal: %v", err)
	}
	for _, id := range []string{"", "a/b", "a\nb"} {
		if _, err := client.Principal(ctx, id); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("invalid principal %q: %v", id, err)
		}
	}
	if _, err := client.Resolve(ctx, identity.ChannelIdentity{Channel: "display-name", ID: channel.ID}); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("unsupported channel: %v", err)
	}
}

func Both(t *testing.T, server identity.Client, handler func(string) http.Handler, channel identity.ChannelIdentity, known identity.Principal) {
	t.Helper()
	t.Run("inproc", func(t *testing.T) { Run(t, identity.NewInProc(server), channel, known) })
	t.Run("http", func(t *testing.T) {
		const token = "conformance-token"
		srv := httptest.NewServer(handler(token))
		defer srv.Close()
		Run(t, identity.NewHTTPClient(srv.URL, token, srv.Client()), channel, known)
		if _, err := identity.NewHTTPClient(srv.URL, "wrong", srv.Client()).Principal(context.Background(), known.ID); !errors.Is(err, identity.ErrForbidden) {
			t.Fatalf("wrong token: %v", err)
		}
	})
}
