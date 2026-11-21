package supervisor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestGroupApprovalRequiresCurrentGroupScopeAndPolicy(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("policy check reached Docker")
		return nil, nil
	}, nil)
	publishScopeKind(t, root, "group")
	settings, err := stack.ReadEnvironment(context.Background(), supTestReader{root}, root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultGlobalSkillsDir(filepath.Join(filepath.Dir(filepath.Dir(root)), "config", "skills"))
	policy := stack.PolicyVersion(settings)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", policy), UserID: "alice", ActorID: "alice", ScopeID: "group:alice", OrganizationID: "personal"}
	if err := m.authorizeCurrentPolicy(context.Background(), request); err != nil {
		t.Error("current group policy rejected", err)
	}
	request.PolicyVersion = "foreign"
	if m.authorizeCurrentPolicy(context.Background(), request) == nil {
		t.Error("foreign group policy accepted")
	}
	request.PolicyVersion = policy
	publishScopeKind(t, root, "user")
	if m.authorizeCurrentPolicy(context.Background(), request) == nil {
		t.Error("group approval switched to personal home")
	}
}
