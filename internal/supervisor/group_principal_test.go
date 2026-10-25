package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestGroupCannotBindAnotherPrincipalToItsHome(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, nil)
	if err := os.WriteFile(filepath.Join(root, "scope.yaml"), []byte("kind: group\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := identity.TelegramEnvelope("bob", 11, "alice", "policy-1")
	env.ContextID = "alice"
	r := hubruntime.ExecuteRequest{Envelope: env, UserID: "alice", ActorID: "bob", ScopeID: "group:alice", OrganizationID: "personal"}
	if _, err := m.bindingFor(r); err == nil {
		t.Fatal("bob acquired alice's group runtime and data roots")
	}
}
