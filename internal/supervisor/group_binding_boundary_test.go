package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestGroupBindingRejectsMismatchedAuthorityFields(t *testing.T) {
	for _, mode := range []string{"user", "actor", "runtime", "scope-record"} {
		t.Run(mode, func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, nil)
			scopeID := "alice"
			r := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "group:alice", OrganizationID: "personal"}
			switch mode {
			case "user":
				r.UserID = "bob"
			case "actor":
				r.ActorID = "bob"
			case "runtime":
				r.RuntimeID = "bob"
			case "scope-record":
				scopeID = "bob"
			}
			if err := os.WriteFile(filepath.Join(root, "scope.yaml"), []byte("kind: group\nid: "+scopeID+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.bindingFor(r); err == nil {
				t.Fatalf("group accepted mismatched %s", mode)
			}
		})
	}
}
