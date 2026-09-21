package credentialbroker

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestClientSignsAndRejectsUnsafeKeyFiles(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		actor, err := (brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{
			"toolhub": {PublicKey: pub, Issuer: "hermes-toolhub", Audiences: []string{"broker:control"}},
		}}).Verify(token, "broker:control", r.Method, r.URL.RequestURI(), nil)
		if err != nil || actor.PrincipalID != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if _, err := (Config{}).New(auth, "broker:control"); err == nil {
		t.Fatal("disabled broker accepted")
	}
	if _, err := (Config{}).NewForBinding(auth, "broker:runtime", "binding-1", "workload-1"); err == nil {
		t.Fatal("disabled runtime broker accepted")
	}
	c, err := (Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}).New(auth, "broker:control")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(t.Context(), "GET", "/v1/requests/request_1", nil, nil); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "bad.private")
	if err := os.WriteFile(badPath, []byte("not-a-private-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Config{URL: server.URL, KeyFile: badPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}).New(auth, "broker:control"); err == nil {
		t.Fatal("invalid signing key accepted")
	}
	if _, err := (Config{URL: server.URL, KeyFile: "relative.key", KeyID: "toolhub", Issuer: "hermes-toolhub"}).New(auth, "broker:control"); err == nil {
		t.Fatal("relative signing key accepted")
	}
	if _, err := (Config{URL: server.URL, KeyFile: filepath.Join(t.TempDir(), "missing.key"), KeyID: "toolhub", Issuer: "hermes-toolhub"}).New(auth, "broker:control"); err == nil {
		t.Fatal("missing signing key accepted")
	}
	if _, err := (Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}).NewForBinding(auth, "broker:runtime", "binding-1", "workload-1"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFromEnvAndActor(t *testing.T) {
	if _, err := FromEnv(""); err == nil {
		t.Fatal("empty env prefix accepted")
	}
	for _, name := range []string{"URL", "KEY_FILE", "KEY_ID", "ISSUER"} {
		t.Setenv("BROKER_TEST_"+name, "")
	}
	if got, err := FromEnv("BROKER_TEST_"); err != nil || got.Enabled() {
		t.Fatalf("disabled config: %#v %v", got, err)
	}
	t.Setenv("BROKER_TEST_URL", "https://broker.example")
	if _, err := FromEnv("BROKER_TEST_"); err == nil {
		t.Fatal("incomplete broker config accepted")
	}
	t.Setenv("BROKER_TEST_KEY_FILE", "/run/secrets/broker.key")
	t.Setenv("BROKER_TEST_KEY_ID", "toolhub")
	t.Setenv("BROKER_TEST_ISSUER", "hermes-toolhub")
	got, err := FromEnv("BROKER_TEST_")
	if err != nil || got.URL != "https://broker.example" || got.KeyID != "toolhub" || got.Issuer != "hermes-toolhub" {
		t.Fatalf("broker config: %#v %v", got, err)
	}
	if got.HTTPClient != nil {
		t.Fatal("HTTPClient set without a CA file")
	}
	t.Setenv("BROKER_TEST_CA_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := FromEnv("BROKER_TEST_"); err == nil {
		t.Fatal("unreadable CA file accepted")
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, []byte("not-a-certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROKER_TEST_CA_FILE", caPath)
	if _, err := FromEnv("BROKER_TEST_"); err == nil {
		t.Fatal("invalid CA bundle accepted")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer tlsServer.Close()
	pem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw})
	if err := os.WriteFile(caPath, pem, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = FromEnv("BROKER_TEST_")
	if err != nil || got.HTTPClient == nil {
		t.Fatalf("CA file client missing: %v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tlsServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := got.HTTPClient.Do(request)
	if err != nil {
		t.Fatalf("self-signed broker CA rejected: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	actor := Actor(auth, "binding-1", "workload-1")
	if actor.PrincipalID != "alice" || actor.BindingID != "binding-1" || actor.WorkloadID != "workload-1" || actor.RuntimeID != "runtime" {
		t.Fatalf("actor mapping: %#v", actor)
	}
}
