package stack

import (
	"strings"
	"testing"
)

func TestGroupSpaceCarriesScopeKindToRuntime(t *testing.T) {
	if err := (Scope{Kind: "group", ID: "team"}).Validate(); err != nil {
		t.Fatalf("explicit group namespace must be valid: %v", err)
	}
	dir := t.TempDir()
	docs := managedDocs("team")
	docs["stack"] = strings.Replace(docs["stack"], "identity: {user: team", "identity: {user: team, kind: group", 1)
	publishManagedDocs(t, dir, "team", "r-0123456789abcdef", docs)
	s, err := ReadEffective(dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	env := RuntimeService(s, "", dir)["environment"].(M)
	if env["HUB_SCOPE_KIND"] != "group" || env["HUB_USER_ID"] != "team" || env["HUB_CONTEXT_ID"] != "team" {
		t.Fatalf("group runtime lost its authority boundary: kind=%v user=%v context=%v", env["HUB_SCOPE_KIND"], env["HUB_USER_ID"], env["HUB_CONTEXT_ID"])
	}
}
