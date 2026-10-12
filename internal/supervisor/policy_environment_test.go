package supervisor

import (
	"context"
	"os"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
)

func TestCurrentPolicyUsesSelectedManagerEnvironment(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, os.ErrNotExist }, func(context.Context, string, string) error { return nil })
	_ = controlTestPolicy(t, root)
	m.cfg.Environment = "dev"
	t.Setenv("HUB_ENV", "prod")
	settings, err := stack.ReadEnvironment(root, "dev")
	if err != nil {
		t.Fatal(err)
	}
	r := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", stack.PolicyVersion(settings)), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Trigger: "cron"}
	if err := m.authorizeCurrentPolicy(r); err != nil {
		t.Fatal("dev policy rejected by prod process default", err)
	}
	r.PolicyVersion = "foreign-policy"
	if m.authorizeCurrentPolicy(r) == nil {
		t.Fatal("foreign policy accepted")
	}
}
