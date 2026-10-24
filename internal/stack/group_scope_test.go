package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGroupSpaceCarriesScopeKindToRuntime(t *testing.T) {
	if err := (Scope{Kind: "group", ID: "team"}).Validate(); err != nil {
		t.Fatalf("explicit group namespace must be valid: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "spaces", "team")
	if err := InitEnvironment(dir, "team", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: group\nid: team\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEnvironment(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	env := RuntimeService(s, "", dir)["environment"].(M)
	if env["HUB_SCOPE_KIND"] != "group" || env["HUB_USER_ID"] != "team" || env["HUB_CONTEXT_ID"] != "team" {
		t.Fatalf("group runtime lost its authority boundary: kind=%v user=%v context=%v", env["HUB_SCOPE_KIND"], env["HUB_USER_ID"], env["HUB_CONTEXT_ID"])
	}
}
