package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
)

// writeModelsLoginDocs publishes one complete test revision: the cliproxy
// service google-pro-1 on instance cliproxy-google-1 plus the five sibling
// stubs a real publish always carries.
func writeModelsLoginDocs(t *testing.T, dir string) {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", "r-x")
	if err := os.MkdirAll(revDir, 0700); err != nil {
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
		if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// `hubctl models login` resolves the service's broker refs, mints idempotent
// enrollment requests, grants ready credentials to the instance workload and
// records grant ids in generated/model-grants.json. Plaintext session files
// under the instance bind are removed once the grants exist.
func TestModelsLoginGrantsReadyCredentialsAndCleansBind(t *testing.T) {
	dir := t.TempDir()
	writeModelsLoginDocs(t, dir)
	auths := filepath.Join(dir, "cliproxy", "cliproxy-google-1", "auths")
	if err := os.MkdirAll(auths, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(auths, "session.json")
	if err := os.WriteFile(stale, []byte(`{"token":"SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	emailNamed := filepath.Join(auths, "antigravity-x@gmail.com.json")
	if err := os.WriteFile(emailNamed, []byte(`{"token":"SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	seededKey := filepath.Join(dir, "cliproxy", "cliproxy-google-1", "api-key")
	if err := os.WriteFile(seededKey, []byte("SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	broker := &fakeModelsBroker{
		requests: map[string]brokerv1.Request{
			"cliproxy-session": {ID: "req-1", Status: "ready", CredentialID: "cred-sess"},
			"model-api-key":    {ID: "req-2", Status: "ready", CredentialID: "cred-key"},
		},
	}
	report, err := modelsLogin(context.Background(), dir, "google-pro-1", broker, broker)
	if err != nil {
		t.Fatal(err)
	}
	if report.Session.GrantID != "g-sess" || report.ProxyAccess.Init.GrantID != "g-key" || report.ProxyAccess.Relay.GrantID != "g-key" {
		t.Fatalf("grants: %+v", report)
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
	if refs["google-one-1"]["init"] != "g-sess" || refs["cliproxy-google-1"]["init"] != "g-key" || refs["cliproxy-google-1"]["relay"] != "g-key" {
		t.Fatalf("grant map must carry the per-consumer grant ids by ref: %s", raw)
	}
	for _, gone := range []string{stale, emailNamed, seededKey} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("plaintext credential material must be removed after enrollment: %s", gone)
		}
	}
	// The broker admits one dedicated credential per (binding, context,
	// principal): the session binds the instance, the key binds its own slot
	// for the init companion plus the relay hop binding for model-relay.
	want := map[string]string{"session": "cliproxy-google-1", "proxy_access": "cliproxy-google-1-key", "proxy_access_relay:model-relay-d84bbb323fab1aed32ec71ad": "model-relay-d84bbb323fab1aed32ec71ad"}
	seen := map[string]string{}
	for _, g := range broker.grants {
		kind := strings.TrimPrefix(g.IdempotencyKey, "models-login:grant:google-pro-1:")
		if seen[kind] != "" || g.BindingID != want[kind] || g.WorkloadID != want[kind] {
			t.Fatalf("grant %s must pin its consumer workload: %+v", kind, g)
		}
		seen[kind] = g.BindingID
	}
	if len(seen) != len(want) {
		t.Fatalf("three grants expected (session, key-init, key-relay): %+v", broker.grants)
	}
}

// The owner's own cliproxy instance reads the cliproxy/ bind root itself —
// compose only namespaces sibling instances under cliproxy/<name>. Seeded
// plaintext there is retired the same way after enrollment.
func TestModelsLoginCleansOwnerInstanceBindRoot(t *testing.T) {
	dir := t.TempDir()
	revDir := filepath.Join(dir, "generated", "passports", "r-x")
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-x}\n"
	docs := map[string]string{
		"stack.yaml":     head + "kind: stack\nidentity: {user: alice}\n",
		"config.yaml":    head + "kind: config\n",
		"models.yaml":    head + "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-alice:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-alice}}\n",
		"workspace.yaml": head + "kind: workspace\n",
		"tools.yaml":     head + "kind: tools\n",
		"grants.yaml":    head + "kind: grants\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auths := filepath.Join(dir, "cliproxy", "auths")
	if err := os.MkdirAll(auths, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(auths, "antigravity-x@gmail.com.json")
	if err := os.WriteFile(stale, []byte(`{"token":"SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	seededKey := filepath.Join(dir, "cliproxy", "api-key")
	if err := os.WriteFile(seededKey, []byte("SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	broker := &fakeModelsBroker{
		requests: map[string]brokerv1.Request{
			"cliproxy-session": {ID: "req-1", Status: "ready", CredentialID: "cred-sess"},
			"model-api-key":    {ID: "req-2", Status: "ready", CredentialID: "cred-key"},
		},
	}
	if _, err := modelsLogin(context.Background(), dir, "google-pro-1", broker, broker); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{stale, seededKey} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("plaintext credential material must be removed after enrollment: %s", gone)
		}
	}
}

// A half-published revision is a broken contract: login refuses to mint
// broker requests off a space whose sibling passports cannot load.
func TestModelsLoginRejectsIncompleteRevision(t *testing.T) {
	dir := t.TempDir()
	revDir := filepath.Join(dir, "generated", "passports", "r-x")
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: alice, environment: dev, revision: r-x}\n"
	models := head + "kind: models\nservices:\n  google-pro-1:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n"
	if err := os.WriteFile(filepath.Join(revDir, "models.yaml"), []byte(models), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	broker := &fakeModelsBroker{requests: map[string]brokerv1.Request{}}
	if _, err := modelsLogin(context.Background(), dir, "google-pro-1", broker, broker); err == nil || !strings.Contains(err.Error(), "load effective") {
		t.Fatalf("incomplete revision must fail the load: %v", err)
	}
	if len(broker.grants) != 0 {
		t.Fatal("no grant may be issued off a partial contract")
	}
}

type fakeModelsBroker struct {
	requests   map[string]brokerv1.Request
	requestLog []brokerv1.CreateRequest
	grants     []brokerv1.GrantRequest
}

func (f *fakeModelsBroker) CreateRequest(_ context.Context, in brokerv1.CreateRequest) (brokerv1.Request, error) {
	f.requestLog = append(f.requestLog, in)
	r, ok := f.requests[in.ContractID]
	if !ok {
		return brokerv1.Request{}, errors.New("unknown contract")
	}
	return r, nil
}

func TestModelsLoginReusesExistingCredentialForSecondRoute(t *testing.T) {
	dir := t.TempDir()
	writeModelsLoginDocs(t, dir)
	broker := &fakeModelsBroker{requests: map[string]brokerv1.Request{
		"cliproxy-session": {Status: "ready", CredentialID: "cred-sess"},
		"model-api-key":    {Status: "ready", CredentialID: "cred-key"},
	}}
	if _, err := modelsLogin(context.Background(), dir, "google-pro-1", broker, broker); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "generated", "passports", "r-x", "models.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := "  google-vision:\n    via: cliproxy\n    endpoint: http://cliproxy-google-1:8317/v1\n    auth: {upstream: {kind: oauth-session, ref: google-one-1}, proxy_access: {kind: broker-key, ref: cliproxy-google-1}}\n"
	if err := os.WriteFile(path, append(raw, []byte(second)...), 0600); err != nil {
		t.Fatal(err)
	}
	broker.requestLog = nil
	if _, err := modelsLogin(context.Background(), dir, "google-vision", broker, broker); err != nil {
		t.Fatal(err)
	}
	for _, request := range broker.requestLog {
		if !strings.HasPrefix(request.IdempotencyKey, "models-login:google-pro-1:") {
			t.Fatalf("enrollment did not reuse existing credential: %s", request.IdempotencyKey)
		}
	}
}

func (f *fakeModelsBroker) Grant(_ context.Context, credentialID string, in brokerv1.GrantRequest) (brokerv1.Grant, error) {
	f.grants = append(f.grants, in)
	switch credentialID {
	case "cred-sess":
		return brokerv1.Grant{ID: "g-sess"}, nil
	case "cred-key":
		return brokerv1.Grant{ID: "g-key"}, nil
	}
	return brokerv1.Grant{}, errors.New("unknown credential")
}
