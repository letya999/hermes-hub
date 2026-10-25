package supervisor

import (
	"context"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupApprovalRequiresCurrentGroupScopeAndPolicy(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("policy check reached Docker")
		return nil, nil
	}, nil)
	policy := controlTestPolicy(t, root)
	name := filepath.Join(root, "scope.yaml")
	if err := os.WriteFile(name, []byte("kind: group\nid: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", policy), UserID: "alice", ActorID: "alice", ScopeID: "group:alice", OrganizationID: "personal"}
	if err := m.authorizeCurrentPolicy(request); err != nil {
		t.Error("current group policy rejected", err)
	}
	request.PolicyVersion = "foreign"
	if m.authorizeCurrentPolicy(request) == nil {
		t.Error("foreign group policy accepted")
	}
	request.PolicyVersion = policy
	os.WriteFile(name, []byte("kind: user\nid: alice\n"), 0600)
	if m.authorizeCurrentPolicy(request) == nil {
		t.Error("group approval switched to personal home")
	}
}
