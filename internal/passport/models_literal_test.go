package passport_test

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// auth.proxy_access is a Broker contract ref shaped {kind, ref} — a literal
// key in a passport is a compile error, never a credential plane leak.
func TestModelsRejectsLiteralCredential(t *testing.T) {
	in := fixtureInputs(t)
	svc := in.PlatformCeiling.Docs["models"]["services"].(map[string]any)
	svc["cliproxy-bad"] = map[string]any{
		"via":      "cliproxy",
		"endpoint": "http://cliproxy:8317/v1",
		"auth": map[string]any{
			"proxy_access": "sk-plaintext-not-a-ref",
		},
	}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("literal proxy_access string compiled")
	} else if !strings.Contains(err.Error(), "auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}
