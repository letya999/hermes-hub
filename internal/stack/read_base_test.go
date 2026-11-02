package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The intent view ignores the published passport: a stale revision that
// claims cliproxy shared must not mask the operator profile's dedicated
// placement before the migration re-derives the services map.
func TestReadBaseKeepsProfileCliproxyIntent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", "r-0123456789abcdef"), 0700); err != nil {
		t.Fatal(err)
	}
	stackDoc := `apiVersion: six-passport/v2
kind: stack
metadata: {space: dana, environment: dev, revision: r-0123456789abcdef}
identity: {user: dana, organization: ""}
services:
    cliproxy: {mode: shared}
    hermes-runtime: {mode: supervised}
`
	for doc, body := range map[string]string{
		"stack.yaml":     stackDoc,
		"config.yaml":    "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"models.yaml":    "apiVersion: six-passport/v2\nkind: models\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"tools.yaml":     "apiVersion: six-passport/v2\nkind: tools\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"grants.yaml":    "apiVersion: six-passport/v2\nkind: grants\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "r-0123456789abcdef", doc), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(root, "spaces", "profiles.yaml"), map[string]any{
		"profiles": map[string]any{
			"owners": map[string]any{"owners": []string{"dana"}, "cliproxy": "dedicated", "cliproxy_credentials": "private"},
		},
	})
	s, err := ReadBase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cliproxy != "dedicated" || s.CliproxyCredentials != "private" {
		t.Fatalf("intent view lost the profile placement: cliproxy=%q credentials=%q", s.Cliproxy, s.CliproxyCredentials)
	}
}

// The governed topology wins over profile leftovers: when the published
// revision keeps cliproxy shared, a private-credentials claim has no
// placement to attach to — the read drops it rather than fail the space.
func TestReadDropsPrivateCredentialsOnSharedPassport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	if err := os.MkdirAll(filepath.Join(dir, "generated", "passports", "r-0123456789abcdef"), 0700); err != nil {
		t.Fatal(err)
	}
	stackDoc := `apiVersion: six-passport/v2
kind: stack
metadata: {space: dana, environment: dev, revision: r-0123456789abcdef}
identity: {user: dana, organization: ""}
services:
    cliproxy: {mode: shared}
    hermes-runtime: {mode: supervised}
`
	for doc, body := range map[string]string{
		"stack.yaml":     stackDoc,
		"config.yaml":    "apiVersion: six-passport/v2\nkind: config\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\nruntime: {timezone: UTC}\nports: {oauth-callback: 8002, browser-vnc: 6081}\n",
		"models.yaml":    "apiVersion: six-passport/v2\nkind: models\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\ndefault: m\nservices: {}\nroutes: {}\n",
		"workspace.yaml": "apiVersion: six-passport/v2\nkind: workspace\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"tools.yaml":     "apiVersion: six-passport/v2\nkind: tools\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
		"grants.yaml":    "apiVersion: six-passport/v2\nkind: grants\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "r-0123456789abcdef", doc), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saveYAML(t, filepath.Join(root, "spaces", "profiles.yaml"), map[string]any{
		"profiles": map[string]any{
			"owners": map[string]any{"owners": []string{"dana"}, "cliproxy": "dedicated", "cliproxy_credentials": "private"},
		},
	})
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cliproxy != "shared" {
		t.Fatalf("published topology must win over the profile: %q", s.Cliproxy)
	}
	if s.CliproxyCredentials == "private" {
		t.Fatalf("private credentials without dedicated placement must not survive the read")
	}
}
