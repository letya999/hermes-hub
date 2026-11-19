// Package conformance checks identity/v1 transports and their real owners.
package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	links, err := client.Bindings(ctx, known.ID)
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	found := false
	for _, link := range links {
		if link.PrincipalID != known.ID {
			t.Fatal("foreign principal binding")
		}
		if link.Channel == channel {
			found = true
		}
	}
	if !found {
		t.Fatal("resolved channel missing from verified bindings")
	}
	if _, err := client.Bindings(ctx, known.ID+"-missing"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("missing binding owner: %v", err)
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
	query := identity.TemplateRequest{Subject: known.ID, Actor: "agent:" + known.ID + ":default", Space: known.ID}
	template, err := client.Template(ctx, query)
	want := identity.AgentTemplate{SpaceKind: "private", OrganizationID: known.OrganizationID, Subject: known, Actor: identity.Principal{ID: query.Actor, Kind: identity.Agent, OwnerID: known.ID, OrganizationID: known.OrganizationID}, Space: known.ID, Restrictions: identity.Restrictions{Mode: "ask-critical"}}
	if err != nil || !reflect.DeepEqual(template, want) {
		t.Fatalf("default template: %+v %v; want %+v", template, err, want)
	}
	query.Actor = "agent:foreign:default"
	if _, err := client.Template(ctx, query); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("foreign template actor: %v", err)
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
