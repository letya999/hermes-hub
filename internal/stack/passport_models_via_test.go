package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The relay decides which declared limits a service's own telemetry can
// measure (SPEC-0092 R7.3): the registry carries each service's via and
// upstream credential kind so provider_quota activates only for cliproxy
// services holding an OAuth session.
func TestRegistryCarriesServiceTelemetryClass(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	user := filepath.Join(spaces, "artem_two")
	rev := "r-testvia"
	revDir := filepath.Join(user, "generated", "passports", rev)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := func(kind string) string {
		return "apiVersion: six-passport/v2\nkind: " + kind + "\nmetadata:\n  space: artem_two\n  environment: dev\n  revision: " + rev + "\n  catalogs: \"5\"\n"
	}
	docs := map[string]string{
		"stack":     "identity:\n  user: artem_two\nservices: {}\n",
		"config":    "runtime:\n  timezone: UTC\n",
		"workspace": "writable:\n  - /workspace\n",
		"tools":     "core:\n  files:\n    via: toolhub\n",
		"grants":    "allow:\n  models:\n    - list\n",
		"models": `default: everyday
routes:
  everyday:
    ladder: [google-pro-1]
services:
  google-pro-1:
    via: cliproxy
    endpoint: http://cliproxy-google-1:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: google-one-1}
      proxy_access: {kind: broker-key, ref: cliproxy-google-1}
    sharing: personal
    models:
      - {id: gemini-3.8-flash-high, reasoning: high}
`,
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(head(name)+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(user, "generated", "passports", "current"), []byte(rev+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistryFor(user, testLoadEffective(t, user))
	if err != nil {
		t.Fatalf("registry from published models doc: %v", err)
	}
	p := reg.Providers["google-pro-1"]
	if p.Via != "cliproxy" {
		t.Fatalf("service via not carried to the registry: %+v", p)
	}
	if p.UpstreamKind != "oauth-session" {
		t.Fatalf("upstream credential kind not carried: %+v", p)
	}
}
