package identity

import (
	"testing"

	primitive "github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/shared/auth"
)

// shared/auth cannot import internal/identity, so its ID rule is a copy;
// this keeps the two from drifting.
func TestSharedAuthIDRuleMatchesIdentityPrimitive(t *testing.T) {
	for _, id := range []string{"alice", "a", "a-b_c9", "", "Alice", "9a", "a.b", "a b", "abcdefghijklmnopqrstuvwxyzabcdefghijklmn", "abcdefghijklmnopqrstuvwxyzabcdefghijklmno", "agent:alice:x"} {
		if auth.ValidID(id) != primitive.ValidID(id) {
			t.Errorf("%q: shared/auth %v, internal/identity %v", id, auth.ValidID(id), primitive.ValidID(id))
		}
	}
}
