package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsEffectiveModelScopeAndRestartBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	// Non-infra space with a per-space cliproxy: the effective scope must
	// report the dedicated placement and the relay-mediated model endpoint.
	docs := managedInfraDocs("alice", false)
	docs["stack"] = "kind: stack\nidentity: {user: alice, infra: false}\nservices:\n" +
		"  hermes-runtime: {mode: supervised}\n" +
		"  model-relay: {mode: per-space}\n" +
		"  toolhub-relay: {mode: per-space}\n" +
		"  toolhub: {mode: shared}\n" +
		"  credential-broker: {mode: shared}\n" +
		"  cliproxy: {mode: per-space}\n"
	docs["models"] = "kind: models\ndefault: fixture-model\nroutes:\n  fixture-model: {ladder: [cliproxy-alice]}\nservices:\n  cliproxy-alice: {via: cliproxy, endpoint: http://cliproxy-alice:8317/v1, models: [{id: fixture-model}]}\n"
	writeManagedSpaceDocs(t, dir, "alice", docs)
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=private-fixture-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error { return run(t.Context(), []string{"doctor", "--dir", dir}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"fixture-model", "http://model-relay:8318/v1", "dedicated", "alice", "restart"} {
		if !strings.Contains(output, expected) {
			t.Errorf("doctor omitted effective scope %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "private-fixture-value") {
		t.Error("doctor printed provider credential")
	}
}

func TestManagedDoctorIncludesOwnerModelApplyPlan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	writeManagedSpaceFixture(t, dir, "alice")
	if err := os.WriteFile(filepath.Join(dir, "secrets.prod.env"), []byte("OPENAI_API_KEY=private-fixture-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error { return run(t.Context(), []string{"doctor", "--dir", dir}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "registry not rendered") || !strings.Contains(output, "provider env not rendered") {
		t.Errorf("managed doctor omitted apply plan: %s", output)
	}
}

func TestSelectedModelCheckUsesExactCatalogIDAndKeepsKeyPrivate(t *testing.T) {
	const secret = "synthetic-model-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gemini-3.8-flash-high"}]}`)
	}))
	defer server.Close()
	if err := checkSelectedModel(context.Background(), server.URL+"/v1", secret, "gemini-3.8-flash-high", server.Client()); err != nil {
		t.Fatal(err)
	}
	err := checkSelectedModel(context.Background(), server.URL+"/v1", secret, "gemini-3.7-flash-high", server.Client())
	if err == nil || !strings.Contains(err.Error(), "gemini-3.7-flash-high") || strings.Contains(err.Error(), secret) {
		t.Fatalf("missing exact model diagnostic or key leaked: %v", err)
	}
}

func TestDoctorLiveRejectsConfiguredModelMissingFromCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"available-model"}]}`)
	}))
	defer server.Close()
	// The live check fails closed naming the configured model: a catalog
	// that does not carry it is a configuration error, never a pass.
	lookup := func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("no port") }
	err := doctorLiveModel(context.Background(), server.URL+"/v1", "synthetic", "missing-model", "compose.yaml", lookup, server.Client())
	if err == nil || !strings.Contains(err.Error(), "missing-model") {
		t.Fatalf("doctor ignored absent selected model: %v", err)
	}
}

func TestLocalCLIProxyModelURLUsesPrivatePublishedPort(t *testing.T) {
	lookup := func(context.Context, ...string) ([]byte, error) { return []byte("127.0.0.1:8317\n"), nil }
	url, err := resolvePublishedModelURL(context.Background(), "http://cliproxy:8317/v1", "compose.yaml", lookup)
	if err != nil || url != "http://127.0.0.1:8317/v1" {
		t.Fatalf("local provider URL not resolved: %q %v", url, err)
	}
	withComposeWarning := func(context.Context, ...string) ([]byte, error) {
		return []byte("time=now level=warning msg=\"unrelated interpolation warning\"\n127.0.0.1:8317\n"), nil
	}
	url, err = resolvePublishedModelURL(context.Background(), "http://cliproxy:8317/v1", "compose.yaml", withComposeWarning)
	if err != nil || url != "http://127.0.0.1:8317/v1" {
		t.Fatalf("Compose warning obscured published port: %q %v", url, err)
	}
	public := func(context.Context, ...string) ([]byte, error) { return []byte("0.0.0.0:8317\n"), nil }
	if _, err := resolvePublishedModelURL(context.Background(), "http://cliproxy:8317/v1", "compose.yaml", public); err == nil {
		t.Fatal("publicly bound provider port accepted for private diagnostic")
	}
}

func TestDoctorLiveUsesPublishedCLIProxyCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"selected-model"}]}`)
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	lookup := func(context.Context, ...string) ([]byte, error) { return []byte("127.0.0.1:" + port), nil }
	err := doctorLiveModel(context.Background(), "http://cliproxy:8317/v1", "synthetic", "selected-model", "compose.yaml", lookup, server.Client())
	if err != nil {
		t.Fatal(err)
	}
}

func TestDoctorPortLookupLoadsStackSupervisorAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	if err := os.WriteFile(filepath.Join(dir, "supervisor.auth"), []byte("HUB_SUPERVISOR_AUTH=synthetic-local-auth\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lookup := doctorPortLookup(dir, func(_ context.Context, env, args []string) ([]byte, error) {
		if !strings.Contains(strings.Join(env, "\n"), "HUB_SUPERVISOR_AUTH=synthetic-local-auth") {
			t.Fatal("doctor Compose lookup omitted the stack supervisor auth")
		}
		return []byte("127.0.0.1:8317"), nil
	})
	if _, err := lookup(context.Background(), "compose", "port"); err != nil {
		t.Fatal(err)
	}
}
