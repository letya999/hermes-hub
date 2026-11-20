// Package testkit provides foreign-service fixtures and transport conformance
// helpers. It contains no service-domain implementation.
package testkit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Transports runs the same assertions against direct and real HTTP clients.
// The handler belongs to the real service or contracts/<svc>/v1/fake.
func Transports[C any](t *testing.T, direct C, handler http.Handler, remote func(string, *http.Client) C, check func(*testing.T, C)) {
	t.Helper()
	t.Run("inproc", func(t *testing.T) { check(t, direct) })
	t.Run("http", func(t *testing.T) {
		srv := httptest.NewServer(handler)
		defer srv.Close()
		check(t, remote(srv.URL, srv.Client()))
	})
}
