package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokeridentity "github.com/letya999/credential-broker/identity"
)

// `hubctl models login` is the operator enrollment entrypoint: it requires
// --dir and --service and fails with named errors, never the usage list.
func TestRunModelsLoginRequiresDirAndService(t *testing.T) {
	if err := runModels(context.Background(), []string{"login"}); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Fatalf("missing --dir must be named: %v", err)
	}
}

// With dir+service the command reaches the broker client and fails with a
// named error when the control-plane env is unset.
func TestRunModelsLoginRequiresBrokerControlEnv(t *testing.T) {
	for _, name := range []string{"URL", "KEY_FILE", "KEY_ID", "ISSUER"} {
		t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_"+name, "")
	}
	err := runModels(context.Background(), []string{"login", "--dir", t.TempDir(), "--service", "google-pro-1"})
	if err == nil || !strings.Contains(err.Error(), "HUB_CREDENTIAL_BROKER_CONTROL_") {
		t.Fatalf("missing broker env must be named: %v", err)
	}
}

// `hubctl models login` dispatches through the top-level op switch.
func TestHubctlModelsDispatches(t *testing.T) {
	err := run(context.Background(), []string{"models", "login"})
	if err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Fatalf("models login dispatch: %v", err)
	}
}

// runModels reports the enrollment outcome as JSON — pending requests carry
// their reviewed link back to the operator.
func TestRunModelsLoginPrintsReport(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	report := modelsLoginReport{Session: modelsLoginGrant{AuthorizationURL: "https://broker/connect/req-1"}}
	if err := printModelsReport(w, report); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	var out modelsLoginReport
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Session.AuthorizationURL != "https://broker/connect/req-1" {
		t.Fatalf("pending link not reported: %+v", out)
	}
}

// runModels end-to-end: signed control client mints requests, grants ready
// credentials and reports the grant ids.
func TestRunModelsLoginEndToEnd(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "control.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	type call struct {
		actor brokeridentity.Actor
		path  string
		body  map[string]any
	}
	var calls []call
	verify := func(r *http.Request, body []byte) (brokeridentity.Actor, bool) {
		token := r.Header.Get("Authorization")
		a, err := (brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{
			"control": {PublicKey: pub, Issuer: "hermes-toolhub", Audiences: []string{"broker:control"}},
		}}).Verify(token[len("Bearer "):], "broker:control", r.Method, r.URL.RequestURI(), body)
		return a, err == nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		actor, ok := verify(r, raw)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		calls = append(calls, call{actor, r.URL.Path, in})
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/requests":
			cred := "cred-sess"
			if in["contract_id"] == "model-api-key" {
				cred = "cred-key"
			}
			out, _ := json.Marshal(map[string]any{"id": "req-1", "status": "ready", "credential_id": cred})
			_, _ = w.Write(out)
		case strings.HasPrefix(r.URL.Path, "/v1/credentials/") && strings.HasSuffix(r.URL.Path, "/grants"):
			id := "g-" + fmt.Sprint(in["binding_id"])
			out, _ := json.Marshal(map[string]any{"id": id})
			_, _ = w.Write(out)
		default:
			http.Error(w, "unexpected", http.StatusForbidden)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	writeModelsLoginDocs(t, dir)
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_URL", server.URL)
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_KEY_FILE", keyPath)
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_KEY_ID", "control")
	t.Setenv("HUB_CREDENTIAL_BROKER_CONTROL_ISSUER", "hermes-toolhub")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := runModels(context.Background(), []string{"login", "--dir", dir, "--service", "google-pro-1"})
	os.Stdout = old
	_ = w.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	var report modelsLoginReport
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		t.Fatalf("operator report not emitted: %v", err)
	}
	if report.Session.GrantID != "g-cliproxy-google-1" || report.ProxyAccess.Init.GrantID != "g-cliproxy-google-1-key" || report.ProxyAccess.Relay.GrantID != "g-model-relay-google-pro-1" {
		t.Fatalf("grant report missing: %+v", report)
	}
	// Every call presents the space identity; grant calls pin the consumer's
	// own policy — init companion and relay acquire under different versions.
	if len(calls) != 5 {
		t.Fatalf("2 requests + 3 grants expected, got %+v", calls)
	}
	policyByBinding := map[string]string{}
	for _, c := range calls {
		if c.actor.PrincipalID != "alice" || c.actor.ContextID != "alice" || c.actor.RuntimeID != "alice" {
			t.Fatalf("actor envelope must present the space identity the grants pin: %+v", c.actor)
		}
		if strings.HasSuffix(c.path, "/grants") {
			policyByBinding[fmt.Sprint(c.body["binding_id"])] = c.actor.PolicyVersion
		}
	}
	want := map[string]string{
		"cliproxy-google-1":        "cliproxy-materialize",
		"cliproxy-google-1-key":    "cliproxy-materialize",
		"model-relay-google-pro-1": "model-relay",
	}
	for binding, policy := range want {
		if policyByBinding[binding] != policy {
			t.Fatalf("grant for %s must pin policy %s: %+v", binding, policy, policyByBinding)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "generated", "model-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	var grants map[string]map[string]map[string]string
	if err := json.Unmarshal(raw, &grants); err != nil {
		t.Fatal(err)
	}
	refs := grants["google-pro-1"]
	if refs["google-one-1"]["init"] != "g-cliproxy-google-1" || refs["cliproxy-google-1"]["init"] != "g-cliproxy-google-1-key" || refs["cliproxy-google-1"]["relay"] != "g-model-relay-google-pro-1" {
		t.Fatalf("grant map must carry the per-consumer grant ids by ref: %s", raw)
	}
}
