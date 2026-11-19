// Package conformance checks toolsurface/v1 transports and their real owners.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	toolsurface "github.com/letya999/hermes-hub/contracts/toolsurface/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

func Run(t *testing.T, client toolsurface.Client, auth identity.Envelope, known []toolsurface.CatalogEntry) {
	t.Helper()
	want, err := json.Marshal(known)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		got, err := client.Catalog(ctx, auth)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(got)
		if err != nil || string(raw) != string(want) {
			t.Fatalf("catalog: got %s, %v; want %s", raw, err, want)
		}
		if len(got) > 0 && len(got[0].Tools) > 0 {
			got[0].Tools[0] = "client-mutation"
		}
	}
	if _, err := client.Catalog(ctx, identity.Envelope{}); !errors.Is(err, toolsurface.ErrForbidden) {
		t.Fatalf("missing identity: %v", err)
	}
}

func Both(t *testing.T, server toolsurface.Client, handler func(string) http.Handler, auth identity.Envelope, known []toolsurface.CatalogEntry) {
	t.Helper()
	t.Run("inproc", func(t *testing.T) { Run(t, toolsurface.NewInProc(server), auth, known) })
	t.Run("http", func(t *testing.T) {
		const token = "conformance-token"
		srv := httptest.NewServer(handler(token))
		defer srv.Close()
		Run(t, toolsurface.NewHTTPClient(srv.URL, token, srv.Client()), auth, known)
		if _, err := toolsurface.NewHTTPClient(srv.URL, "wrong", srv.Client()).Catalog(context.Background(), auth); !errors.Is(err, toolsurface.ErrForbidden) {
			t.Fatalf("wrong token: %v", err)
		}
	})
}
