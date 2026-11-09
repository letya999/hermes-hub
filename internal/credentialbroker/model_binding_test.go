package credentialbroker

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestModelRelayBindingIsolation(t *testing.T) {
	seen := map[string]bool{}
	for _, pair := range [][2]string{{"alice", "cliproxy"}, {"bob", "cliproxy"}, {"a-b", "c"}, {"a", "b-c"}, {strings.Repeat("a", 128), strings.Repeat("b", 128)}} {
		id := ModelRelayBinding(pair[0], pair[1])
		if !identity.ValidID(id) || seen[id] || id != ModelRelayBinding(pair[0], pair[1]) {
			t.Fatal("invalid or colliding model workload identity")
		}
		seen[id] = true
	}
}

func TestMediaBindingIsolation(t *testing.T) {
	alice, bob := MediaBinding("alice"), MediaBinding("bob")
	if alice == bob || alice != MediaBinding("alice") || !identity.ValidID(alice) || !identity.ValidID(bob) || len(MediaBinding(strings.Repeat("a", 128))) != len(alice) {
		t.Fatal("media workloads must be stable, bounded and distinct per infra owner")
	}
}
