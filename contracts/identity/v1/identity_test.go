package identity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
)

type fixture struct{}

func (fixture) Bindings(_ context.Context, owner string) ([]identity.ChannelBinding, error) {
	if owner != "alice" {
		return nil, identity.ErrNotFound
	}
	return []identity.ChannelBinding{{ID: "fixture-link", PrincipalID: owner, Channel: identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}}}, nil
}

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

func (fixture) Template(_ context.Context, query identity.TemplateRequest) (identity.AgentTemplate, error) {
	if query != (identity.TemplateRequest{Subject: "alice", Actor: "agent:alice:default", Space: "alice"}) {
		return identity.AgentTemplate{}, identity.ErrForbidden
	}
	return identity.AgentTemplate{SpaceKind: "private", Subject: identity.Principal{ID: "alice", Kind: identity.Human}, Actor: identity.Principal{ID: "agent:alice:default", Kind: identity.Agent, OwnerID: "alice"}, Space: "alice", Restrictions: identity.Restrictions{Mode: "ask-critical"}}, nil
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	server := fixture{}
	conformance.Both(t, server, func(token string) http.Handler { return identity.Handler(server, token) }, identity.ChannelIdentity{Channel: identity.Telegram, ID: "7"}, identity.Principal{ID: "alice", Kind: identity.Human})
}

type templateFixture struct {
	fixture
	value identity.AgentTemplate
}

func (f templateFixture) Template(context.Context, identity.TemplateRequest) (identity.AgentTemplate, error) {
	return f.value, nil
}

func TestTemplateTransportCopiesProfileLists(t *testing.T) {
	query := identity.TemplateRequest{Subject: "alice", Actor: "agent:alice:default", Space: "alice"}
	value, _ := (fixture{}).Template(context.Background(), query)
	value.Restrictions.Skills = []string{"reader"}
	value.Restrictions.MemoryScopes = []string{}
	server := templateFixture{value: value}
	httpServer := httptest.NewServer(identity.Handler(server, "token"))
	defer httpServer.Close()
	for _, client := range []identity.Client{identity.NewInProc(server), identity.NewHTTPClient(httpServer.URL, "token", httpServer.Client())} {
		got, err := client.Template(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		got.Restrictions.Skills[0] = "client-mutation"
		again, err := client.Template(context.Background(), query)
		if err != nil || again.Restrictions.Skills[0] != "reader" || again.Restrictions.MemoryScopes == nil {
			t.Fatalf("template isolation: %+v %v", again, err)
		}
	}
}

func TestTemplateRefusesAliasedServerResult(t *testing.T) {
	query := identity.TemplateRequest{Subject: "alice", Actor: "agent:alice:default", Space: "alice"}
	for _, mutate := range []func(*identity.AgentTemplate){func(v *identity.AgentTemplate) { v.Space = "bob" }, func(v *identity.AgentTemplate) { v.Actor.OwnerID = "bob" }, func(v *identity.AgentTemplate) { v.Restrictions.Mode = "unknown" }} {
		value, _ := (fixture{}).Template(context.Background(), query)
		mutate(&value)
		server := templateFixture{value: value}
		httpServer := httptest.NewServer(identity.Handler(server, "token"))
		for _, client := range []identity.Client{identity.NewInProc(server), identity.NewHTTPClient(httpServer.URL, "token", httpServer.Client())} {
			if _, err := client.Template(context.Background(), query); !errors.Is(err, identity.ErrUnavailable) {
				t.Fatalf("invalid template accepted: %v", err)
			}
		}
		httpServer.Close()
	}
}
