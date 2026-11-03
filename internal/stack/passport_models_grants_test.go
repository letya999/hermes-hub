package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The hop credential is the enrolled Broker grant id, not the ref name: the
// registry resolves each service's credential ref through
// generated/model-grants.json. A service without an enrolled grant keeps the
// ref and fails closed at acquire time.
func TestRegistryResolvesEnrolledGrantIDs(t *testing.T) {
	user := filepath.Join(t.TempDir(), "spaces", "artem_two")
	rev := "r-grants"
	revDir := filepath.Join(user, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: artem_two, environment: dev, revision: " + rev + ", catalogs: \"5\"}\n"
	docs := map[string]string{
		"stack.yaml":  head + "kind: stack\nidentity: {user: artem_two}\n",
		"config.yaml": head + "kind: config\n",
		"models.yaml": head + `kind: models
routes:
  everyday: {ladder: [google-pro-1]}
services:
  google-pro-1:
    via: cliproxy
    endpoint: http://cliproxy-google-1:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: google-one-1}
      proxy_access: {kind: broker-key, ref: cliproxy-google-1}
    models:
      - {id: gemini-3.8-flash-high}
`,
		"workspace.yaml": head + "kind: workspace\n",
		"tools.yaml":     head + "kind: tools\n",
		"grants.yaml":    head + "kind: grants\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(user, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	grants := `{"google-pro-1": {"google-one-1": {"init": "g-sess"}, "cliproxy-google-1": {"init": "g-key", "relay": "g-key-r"}}}`
	if err := os.WriteFile(filepath.Join(user, "generated", "model-grants.json"), []byte(grants), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistry(user, Settings{User: "artem_two", Environment: "dev"})
	if err != nil {
		t.Fatalf("registry from published models doc: %v", err)
	}
	gp := reg.Providers["google-pro-1"]
	if gp.BrokerGrants["artem_two"] != "g-key-r" {
		t.Fatalf("hop credential must be the relay-bound grant id: %+v", gp.BrokerGrants)
	}
}
