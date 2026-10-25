package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestSupervisorGroupBindingRequiresDedicatedGroupHome(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, nil)
	path := filepath.Join(root, "scope.yaml")
	if err := os.WriteFile(path, []byte("kind: group\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "group:alice", OrganizationID: "personal"}
	bound, err := m.bindingFor(r)
	if err != nil || bound.ContextRoot != root || bound.PrincipalID != "alice" {
		t.Errorf("dedicated group binding rejected: %+v %v", bound, err)
	}
	if err := os.WriteFile(path, []byte("kind: user\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.bindingFor(r); err == nil {
		t.Error("group request selected a personal home")
	}
}
