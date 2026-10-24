package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestReconcilerBrokerCredentialBackend(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		status         int
		alive, wantErr bool
	}{
		{"active", `{"status":"active"}`, 200, true, false},
		{"revoked", `{"status":"revoked"}`, 200, false, false},
		{"missing-or-invisible", `{"code":"not_found"}`, 404, false, true},
		{"unavailable", `{"code":"unavailable"}`, 503, false, true},
		{"missing-status", `{}`, 200, false, true},
		{"unknown-status", `{"status":"future_state"}`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _ := seededStore(t)
			conn, _ := store.connectionFor("google-work")
			ref, _ := store.credentialFor(conn.CredentialRefID)
			ref.Backend, ref.Locator = "credential-broker", "credential_test"
			ref.BrokerContractID, ref.BrokerContractRevision = "google-token", 1
			ref.Revision++
			ref.CredentialRefID = api.CredentialReferenceID(conn.ConnectionID, ref.Revision)
			if _, err := store.RotateCredentialRecord(ref); err != nil {
				t.Fatal(err)
			}
			conn, _ = store.connectionFor(conn.ConnectionID)
			auth := aliceAuth()
			if err := store.PutOnboarding(api.Onboarding{Schema: api.SchemaVersion, Revision: 1, Mode: api.OnboardingCatalog, OnboardingID: "onboard-broker-check", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, ConnectionID: conn.ConnectionID, Phase: api.PhaseEnabled}); err != nil {
				t.Fatal(err)
			}
			gets, deletes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/credentials/credential_test" {
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if r.Method == http.MethodDelete {
					deletes++
					if tc.status == 404 {
						w.WriteHeader(404)
						_, _ = w.Write([]byte(tc.body))
						return
					}
					w.WriteHeader(200)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method %s", r.Method)
				}
				gets++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			key := filepath.Join(t.TempDir(), "broker.private")
			if err := os.WriteFile(key, private, 0600); err != nil {
				t.Fatal(err)
			}
			local, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "secrets.json"), Key: make([]byte, 32)})
			if err != nil {
				t.Fatal(err)
			}
			c := &ControlPlane{Store: store, Secrets: local, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: key, KeyID: "toolhub", Issuer: "hermes-toolhub"}}
			r := c.NewReconciler()
			alive, err := r.CredAlive(t.Context(), ref.CredentialRefID)
			if alive != tc.alive || (err != nil) != tc.wantErr || gets != 1 {
				t.Errorf("alive=%v error=%v broker reads=%d", alive, err, gets)
			}
			if tc.wantErr {
				r.Notify = func(context.Context, string, string, map[string]any) error {
					t.Error("ambiguous broker response requested reauthorization")
					return nil
				}
				if err := r.ScanOnce(t.Context()); err == nil {
					t.Error("ambiguous broker response was not reported")
				}
				after, _ := store.connectionFor(conn.ConnectionID)
				if after.Status != api.ActiveStatus {
					t.Error("ambiguous response changed connection lifecycle")
				}
			}
			if err := r.CredPurge(t.Context(), conn, ref); err != nil || deletes != 1 {
				t.Fatalf("purge error=%v broker deletes=%d", err, deletes)
			}
		})
	}
}
