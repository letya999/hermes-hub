package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

// The init companion holds two different lease identities: the session grant
// pins the instance slot and the api key's init grant pins <instance>-key —
// the broker admits one dedicated credential per (binding, context,
// principal), so a shared slot would collide.
func TestBrokerCliproxyPlaneAcquiresPerPurposeBindings(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "cliproxy.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	type leaseActor struct {
		grant, binding, workload string
	}
	var leases []leaseActor
	source := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(source, []byte(`{"token":"SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	verifier := brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{
		"cliproxy": {PublicKey: pub, Issuer: "hermes-cliproxy-session", Audiences: []string{"broker:control", "broker:runtime"}},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		audience := "broker:runtime"
		if r.URL.Path == "/v1/leases" {
			audience = "broker:control"
		}
		actor, err := verifier.Verify(r.Header.Get("Authorization")[len("Bearer "):], audience, r.Method, r.URL.RequestURI(), body)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/leases":
			var in brokerv1.AcquireLease
			_ = json.Unmarshal(body, &in)
			leases = append(leases, leaseActor{in.GrantID, actor.BindingID, actor.WorkloadID})
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease-" + in.GrantID, GrantID: in.GrantID, ExpiresAt: time.Now().Add(time.Minute)})
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-g-sess/materialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"mounts": []map[string]any{{"source": source, "target": "/cliproxy/auths/session.json"}}})
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-g-key/materialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"env": map[string]string{"MODEL_API_KEY": "SENTINEL-KEY"}})
		case r.Method == "POST" && (r.URL.Path == "/v1/runtime/leases/lease-g-sess/release" || r.URL.Path == "/v1/runtime/leases/lease-g-key/release"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected", http.StatusForbidden)
		}
	}))
	defer server.Close()
	actor := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", PolicyVersion: "cliproxy-materialize"}
	plane := brokerCliproxyPlane{
		cfg:      credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "cliproxy", Issuer: "hermes-cliproxy-session"},
		actor:    actor,
		instance: "cliproxy-google-1",
	}
	if _, err := plane.File(context.Background(), "g-sess"); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.Env(context.Background(), "g-key", "MODEL_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("two acquires expected: %+v", leases)
	}
	if leases[0].grant != "g-sess" || leases[0].binding != "cliproxy-google-1" || leases[0].workload != "cliproxy-google-1" {
		t.Fatalf("session lease must pin the instance slot: %+v", leases[0])
	}
	if leases[1].grant != "g-key" || leases[1].binding != "cliproxy-google-1-key" || leases[1].workload != "cliproxy-google-1-key" {
		t.Fatalf("key lease must pin its own <instance>-key slot: %+v", leases[1])
	}
}
