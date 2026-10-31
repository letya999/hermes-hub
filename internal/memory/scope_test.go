package memory

import (
	"strings"
	"testing"
)

func TestProviderScopeIsolation(t *testing.T) {
	secret := []byte("scope-material-0123456789abcdef")
	a := MintScope(secret, "prod", "alice", string(AudiencePrivate))
	b := MintScope(secret, "prod", "alice", string(AudienceGroup))
	dev := MintScope(secret, "dev", "alice", string(AudiencePrivate))
	bob := MintScope(secret, "prod", "bob", string(AudiencePrivate))
	if a.Key == b.Key || a.Key == dev.Key || a.Key == bob.Key || b.Key == dev.Key {
		t.Fatal("scope keys must differ across audience, environment and principal")
	}
	if again := MintScope(secret, "prod", "alice", string(AudiencePrivate)); again.Key != a.Key {
		t.Fatal("scope minting must be deterministic")
	}
	if other := MintScope([]byte("different-material-0123456789ab"), "prod", "alice", string(AudiencePrivate)); other.Key == a.Key {
		t.Fatal("scope key must bind to the hub-side secret material")
	}
}

func TestScopeOpaqueNoRawIDs(t *testing.T) {
	scope := MintScope([]byte("material-0123456789abcdef01234"), "prod", "alice", string(AudiencePrivate))
	for _, raw := range []string{"alice", "prod", "user:alice", "private"} {
		if strings.Contains(scope.Key, raw) {
			t.Fatalf("scope key leaks raw identifier %q: %q", raw, scope.Key)
		}
	}
	if len(scope.Key) != 64 {
		t.Fatalf("scope key must be a fixed-length digest, got %q", scope.Key)
	}
}

func TestAudienceOfFailsClosed(t *testing.T) {
	if got := AudienceOf("user:alice"); got != AudiencePrivate {
		t.Fatalf("user scope must be private, got %q", got)
	}
	for _, crafted := range []string{"group:alice", "group:", "", "user", "useralice", "USER:alice", " user:alice", "org:acme", "user :alice", "user : alice", " us", "u ser:alice"} {
		if got := AudienceOf(crafted); got != AudienceGroup {
			t.Fatalf("crafted scope %q minted a private audience", crafted)
		}
	}
}
