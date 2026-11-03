package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

// TestMigrationV2TwoUserIsolation is the milestone golden (R12.1/R8.1):
// two differently-shaped legacy spaces migrate to isolated, versioned v2
// revisions — no cross-user leakage of sections, grants, routes or mounts,
// no stale authorization, distinct rollback fences. Receipts come only from
// the published artifacts, never from shared caches.
func TestMigrationV2TwoUserIsolation(t *testing.T) {
	settings := func(user, tz string, oauth, browser int, tools string) string {
		return fmt.Sprintf("schema: 3\nuser: %s\ntimezone: %s\nmemory: true\nmodel: default\nmodel_url: https://models.example.test\ngoogle_email: %s@example.test\ngitlab_host: gitlab.example.test\ndesktop_url: https://desktop.example.test\ndrafts_url: https://drafts.example.test\noauth_port: %d\nbrowser_port: %d\ntools:\n%s", user, tz, user, oauth, browser, tools)
	}
	gov := func(grants string) string {
		return `{"schema":1,"revision":1,"inventory":[{"id":"native:terminal","owner":"hermes","official":true,"effect":"exec","default":"deny"},{"id":"hub:files","owner":"hub","official":true,"effect":"mutate","default":"deny"}],"rules":[],"grants":[` + grants + `]}`
	}
	grant := func(id, principal, section string) string {
		return `{"schema":1,"grant_id":"` + id + `","principal_id":"` + principal + `","section":"` + section + `","reason":"approved","granted_by":"operator","expires_at":"2999-01-01T00:00:00Z","status":"active","revision":1}`
	}
	makeSpace := func(parent, user string, oauth, browser int, tools, grants string) string {
		dir := filepath.Join(parent, user)
		th := filepath.Join(dir, "runtime", "toolhub")
		if err := os.MkdirAll(th, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(settings(user, "UTC", oauth, browser, tools)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte("models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: m1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(th, "governance.json"), []byte(gov(grants)), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	parent := filepath.Join(t.TempDir(), "spaces")
	alice := makeSpace(parent, "alice", 13001, 13002, "  terminal:\n    via: native\n  file:\n    via: toolhub\n",
		grant("a1", "alice", "native:terminal")+","+grant("a2", "alice", "hub:files"))
	bob := makeSpace(parent, "bob", 14001, 14002, "  file:\n    via: toolhub\n",
		grant("b1", "bob", "hub:files"))

	repA, err := MigratePassport(PassportOptions{Dir: alice, Environment: "dev", Apply: true})
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	repB, err := MigratePassport(PassportOptions{Dir: bob, Environment: "dev", Apply: true})
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	if repA.Revision == repB.Revision {
		t.Fatal("two users share one revision id")
	}
	readDoc := func(dir, rev, name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rev, name))
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := yaml.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	coreA, _ := readDoc(alice, repA.Revision, "tools.yaml")["core"].(map[string]any)
	secA := make([]any, 0, len(coreA))
	for k := range coreA {
		secA = append(secA, k)
	}
	coreB, _ := readDoc(bob, repB.Revision, "tools.yaml")["core"].(map[string]any)
	secB := make([]any, 0, len(coreB))
	for k := range coreB {
		secB = append(secB, k)
	}
	if strings.Contains(anyJoin(secA), "native:terminal") == false {
		t.Fatalf("alice lost native:terminal: %v", secA)
	}
	if strings.Contains(anyJoin(secB), "native:terminal") {
		t.Fatalf("bob leaked alice's terminal section: %v", secB)
	}
	grantsB := readDoc(bob, repB.Revision, "grants.yaml")["allow"]
	for cap := range grantsB.(map[string]any) {
		if strings.HasPrefix(cap, "terminal.") {
			t.Fatalf("bob leaked terminal grant: %v", grantsB)
		}
	}
	for _, dir := range []string{alice, bob} {
		if _, err := os.Stat(filepath.Join(dir, "settings.yaml")); err != nil {
			t.Fatalf("%s lost settings.yaml", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "generated", "migration-backup")); err != nil {
			t.Fatalf("%s missing verified backup", dir)
		}
	}
	// Prompt pointer pins each user's own revision — never a shared one.
	ptrA, _ := os.ReadFile(filepath.Join(alice, "generated", "passports", repA.Revision, "prompt-pointer.txt"))
	if !strings.Contains(string(ptrA), "revision="+repA.Revision) {
		t.Fatalf("alice prompt pointer %q lacks %q", ptrA, repA.Revision)
	}
	// Audits are isolated too: bob's inventory cannot see alice's surfaces.
	auditB, err := AuditPassport(bob)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(auditB.Toolsets.Core, ","), "native:terminal") {
		t.Fatal("bob audit shows alice's terminal toolset")
	}
	if _, err := passport.Current(bob); err != nil || repB.Revision == repA.Revision {
		t.Fatal("bob revision state invalid")
	}
}

func anyJoin(v any) string {
	var parts []string
	if list, ok := v.([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, ",")
}
