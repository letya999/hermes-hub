package passport_test

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// A cliproxy service endpoint must point at a cliproxy instance declared by
// the stack passport — routing to an arbitrary host breaks session isolation.
func TestCliproxyServiceRequiresStackInstance(t *testing.T) {
	in := fixtureInputs(t)
	svc := in.PlatformCeiling.Docs["models"]["services"].(map[string]any)
	svc["rogue"] = map[string]any{
		"via":      "cliproxy",
		"endpoint": "http://evil.example.com:8317/v1",
		"auth": map[string]any{
			"upstream":     map[string]any{"kind": "oauth-session", "ref": "rogue"},
			"proxy_access": map[string]any{"kind": "broker-key", "ref": "rogue"},
		},
	}
	if _, err := passport.Compile(in); err == nil || !strings.Contains(err.Error(), "cliproxy") {
		t.Fatalf("want cliproxy instance error, got %v", err)
	}
}
