package dns

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

// file: refs read a JSON env-map below the config root; broker: refs run
// acquire -> materialize -> release and return the materialized env map
// (SPEC-0076 R3). No credential may surface anywhere else.
func TestResolveCredentials(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "creds.json"), []byte(`{"REGRU_USERNAME":"u","REGRU_PASSWORD":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &Resolver{Root: os.DirFS(root)}
	creds, release, err := res.Resolve(ctx, "file:creds.json")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if creds["REGRU_USERNAME"] != "u" || creds["REGRU_PASSWORD"] != "p" {
		t.Fatalf("file creds: %+v", creds)
	}

	released := &atomic.Int32{}
	srv := fakeDNSBroker(t, map[string]string{"DO_TOKEN": "tok"}, released)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "runtime.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	res = &Resolver{
		Auth:   identity.Envelope{Schema: identity.Schema, PrincipalID: "op", ContextID: "op", RuntimeID: "rt", PolicyVersion: "p"},
		Broker: credentialbroker.Config{URL: srv.URL, KeyFile: keyPath, KeyID: "hermes-runtime", Issuer: "hermes-hub", HTTPClient: srv.Client()},
	}
	creds, release, err = res.Resolve(ctx, "broker:grant-dns-1")
	if err != nil {
		t.Fatal(err)
	}
	if creds["DO_TOKEN"] != "tok" {
		t.Fatalf("broker creds: %+v", creds)
	}
	release()
	if released.Load() != 1 {
		t.Fatal("lease was not released")
	}
}

// file: refs fail closed — a missing file and an undecodable body both map
// to ErrUnavailable and return no credentials.
func TestResolveFileErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &Resolver{Root: os.DirFS(root)}
	for _, tc := range []struct{ ref string }{{"file:missing.json"}, {"file:broken.json"}} {
		creds, release, err := res.Resolve(context.Background(), tc.ref)
		if !errors.Is(err, ErrUnavailable) || creds != nil || release != nil {
			t.Fatalf("%s: want ErrUnavailable with no creds, got %v %v", tc.ref, creds, err)
		}
	}
	// broker: with no broker configured fails before any network call.
	creds, release, err := res.Resolve(context.Background(), "broker:grant-1")
	if err == nil || creds != nil || release != nil {
		t.Fatalf("unconfigured broker must fail closed, got %v", err)
	}
}

func fakeDNSBroker(t *testing.T, env map[string]string, released *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/leases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease-1", GrantID: "grant-dns-1", CredentialID: "cred-1", Revision: 1, ExpiresAt: time.Now().Add(time.Minute)})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/materialize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease-1", ExpiresAt: time.Now().Add(time.Minute), Env: env})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/release", func(w http.ResponseWriter, r *http.Request) {
		released.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
