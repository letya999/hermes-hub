package credentialbroker

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ResolveFile acquires a workload lease, reads the file the materialized
// mount names, releases the lease and returns the payload bytes. The lease
// is always released — including on materialization failure.
func TestResolveFileReadsMountAndReleasesLease(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "cliproxy.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"token":"SENTINEL"}`)
	source := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	released := false
	verify := func(r *http.Request, audience string) bool {
		token := r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_, err := (brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{
			"cliproxy": {PublicKey: pub, Issuer: "hermes-toolhub", Audiences: []string{audience}},
		}}).Verify(token[len("Bearer "):], audience, r.Method, r.URL.RequestURI(), body)
		return err == nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/leases" && verify(r, "broker:control"):
			_, _ = w.Write([]byte(`{"id":"lease-1"}`))
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-1/materialize" && verify(r, "broker:runtime"):
			out, _ := json.Marshal(map[string]any{"mounts": []map[string]any{{"source": source, "target": "/cliproxy/auths/session.json"}}})
			_, _ = w.Write(out)
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-1/release" && verify(r, "broker:runtime"):
			released = true
			// The broker's dropLease removes the materialized directory on
			// release — the payload must already be read by then.
			_ = os.Remove(source)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected", http.StatusForbidden)
		}
	}))
	defer server.Close()
	actor := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	cfg := Config{URL: server.URL, KeyFile: keyPath, KeyID: "cliproxy", Issuer: "hermes-toolhub"}
	got, err := cfg.ResolveFile(t.Context(), actor, "cliproxy-google-1", "cliproxy-google-1", "g-sess")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload: %s", got)
	}
	if !released {
		t.Fatal("lease not released")
	}
}
