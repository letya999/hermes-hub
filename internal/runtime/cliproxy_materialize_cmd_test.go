package runtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

// The cliproxy init companion runs `hub-runtime cliproxy-materialize`; the
// subcommand reads its instance from HUB_CLIPROXY_INSTANCE and fails with a
// named error rather than the generic usage list.
func TestRunCliproxyMaterializeRequiresInstanceEnv(t *testing.T) {
	t.Setenv("HUB_CLIPROXY_INSTANCE", "")
	err := Run([]string{"cliproxy-materialize"})
	if err == nil || !strings.Contains(err.Error(), "HUB_CLIPROXY_INSTANCE") {
		t.Fatalf("expected named env error, got %v", err)
	}
}

// The subcommand end-to-end: broker acquire/materialize/release per grant,
// session file into the auths view, api-keys into the rendered config.
func TestRunCliproxyMaterializeEndToEnd(t *testing.T) {
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
	releases := 0
	verify := func(r *http.Request, audience string) bool {
		token := r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		actor, err := (brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{
			"cliproxy": {PublicKey: pub, Issuer: "hermes-toolhub", Audiences: []string{audience}},
		}}).Verify(token[len("Bearer "):], audience, r.Method, r.URL.RequestURI(), body)
		if err != nil {
			return false
		}
		// Session and proxy key each pin their own dedicated workload slot,
		// derived from the instance plus the credential ref.
		sessionSlot := credentialbroker.CliproxySessionBinding("cliproxy-google-1", "google-one-1")
		keySlot := credentialbroker.CliproxyKeyBinding("cliproxy-google-1", "cliproxy-google-1")
		return (actor.BindingID == sessionSlot || actor.BindingID == keySlot) && actor.WorkloadID == actor.BindingID
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/leases" && verify(r, "broker:control"):
			_, _ = w.Write([]byte(`{"id":"lease-1"}`))
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-1/materialize" && verify(r, "broker:runtime"):
			out, _ := json.Marshal(map[string]any{
				"mounts": []map[string]any{{"source": source, "target": "/cliproxy/auths/session.json"}},
				"env":    map[string]string{"MODEL_API_KEY": "SENTINEL-KEY"},
			})
			_, _ = w.Write(out)
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/lease-1/release" && verify(r, "broker:runtime"):
			releases++
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected", http.StatusForbidden)
		}
	}))
	defer server.Close()
	gen := filepath.Join(t.TempDir(), "generated")
	if err := os.MkdirAll(filepath.Join(gen, "passports", "r-x"), 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-x}\n"
	docs := map[string]string{
		"stack.yaml":     head + "kind: stack\nidentity: {user: alice}\n",
		"config.yaml":    head + "kind: config\n",
		"models.yaml":    head + "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n",
		"workspace.yaml": head + "kind: workspace\n",
		"tools.yaml":     head + "kind: tools\n",
		"grants.yaml":    head + "kind: grants\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(gen, "passports", "r-x", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gen, "passports", "current"), []byte("r-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "model-grants.json"), []byte(`{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "base")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config.yaml"), []byte("port: 8317\nauth-dir: \"/cliproxy/auths\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auths, cfgDir := filepath.Join(t.TempDir(), "auths"), filepath.Join(t.TempDir(), "cfg")
	t.Setenv("HUB_CLIPROXY_INSTANCE", "cliproxy-google-1")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_CLIPROXY_GEN_DIR", gen)
	t.Setenv("HUB_CLIPROXY_BASE_DIR", base)
	t.Setenv("HUB_CLIPROXY_AUTHS_DIR", auths)
	t.Setenv("HUB_CLIPROXY_CFG_DIR", cfgDir)
	t.Setenv("HUB_CREDENTIAL_BROKER_CLIPROXY_URL", server.URL)
	t.Setenv("HUB_CREDENTIAL_BROKER_CLIPROXY_KEY_FILE", keyPath)
	t.Setenv("HUB_CREDENTIAL_BROKER_CLIPROXY_KEY_ID", "cliproxy")
	t.Setenv("HUB_CREDENTIAL_BROKER_CLIPROXY_ISSUER", "hermes-toolhub")
	if err := Run([]string{"cliproxy-materialize"}); err != nil {
		t.Fatal(err)
	}
	// The init companion is recreated by space apply. Its first output is
	// deliberately read-only, so the next materialization must replace it.
	if err := Run([]string{"cliproxy-materialize"}); err != nil {
		t.Fatalf("second materialization: %v", err)
	}
	session, err := os.ReadFile(filepath.Join(auths, "google-one-1.json"))
	if err != nil || !strings.Contains(string(session), "SENTINEL") {
		t.Fatalf("session not delivered: %v", err)
	}
	rendered, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if err != nil || !strings.Contains(string(rendered), "SENTINEL-KEY") {
		t.Fatalf("config not rendered: %v %s", err, rendered)
	}
	if releases != 4 {
		t.Fatalf("leases released: %d", releases)
	}
}
