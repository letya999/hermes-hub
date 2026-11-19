package toolsurface_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	toolsurface "github.com/letya999/hermes-hub/contracts/toolsurface/v1"
	"github.com/letya999/hermes-hub/contracts/toolsurface/v1/conformance"
	"github.com/letya999/hermes-hub/internal/identity"
)

type fixture struct{ entries []toolsurface.CatalogEntry }

func (f fixture) Catalog(_ context.Context, auth identity.Envelope) ([]toolsurface.CatalogEntry, error) {
	if len(auth.Scopes) > 0 {
		auth.Scopes[0] = "server-mutation"
	}
	return f.entries, nil
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	entries := []toolsurface.CatalogEntry{{Name: "fixture", Version: "1.0.0", Tools: []string{"read"}, Status: "active", Enabled: true}}
	server := fixture{entries}
	conformance.Both(t, server, func(token string) http.Handler { return toolsurface.Handler(server, token) }, identity.TelegramEnvelope("alice", 7, "runtime", "policy-1"), entries)
}

func TestServerCannotMutateCallerEnvelope(t *testing.T) {
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.Scopes = []string{"memory-read"}
	server := fixture{}
	srv := httptest.NewServer(toolsurface.Handler(server, "token"))
	defer srv.Close()
	for _, client := range []toolsurface.Client{toolsurface.NewInProc(server), toolsurface.NewHTTPClient(srv.URL, "token", srv.Client())} {
		if _, err := client.Catalog(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		if auth.Scopes[0] != "memory-read" {
			t.Fatal("server changed caller's scope")
		}
	}
}
