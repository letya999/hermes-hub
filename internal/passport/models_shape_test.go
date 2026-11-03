package passport_test

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// The models passport is normative: default names a route, ladders name known
// services, via is the declared enum and reasoning stays in its vocabulary.
func TestModelsPassportNormativeShape(t *testing.T) {
	t.Run("default must name a route", func(t *testing.T) {
		in := fixtureInputs(t)
		in.PlatformTemplate.Docs["models"]["default"] = "ghost"
		if _, err := passport.Compile(in); err == nil || !strings.Contains(err.Error(), "models.default") {
			t.Fatalf("want models.default error, got %v", err)
		}
	})
	t.Run("via enum", func(t *testing.T) {
		in := fixtureInputs(t)
		svc := in.PlatformCeiling.Docs["models"]["services"].(map[string]any)
		svc["weird"] = map[string]any{"via": "carrier-pigeon"}
		if _, err := passport.Compile(in); err == nil || !strings.Contains(err.Error(), "via") {
			t.Fatalf("want via error, got %v", err)
		}
	})
	t.Run("proxy_access forbidden off cliproxy", func(t *testing.T) {
		in := fixtureInputs(t)
		svc := in.PlatformCeiling.Docs["models"]["services"].(map[string]any)
		svc["direct-key"].(map[string]any)["auth"] = map[string]any{
			"proxy_access": map[string]any{"kind": "broker-key", "ref": "x"},
		}
		if _, err := passport.Compile(in); err == nil || !strings.Contains(err.Error(), "proxy_access") {
			t.Fatalf("want proxy_access error, got %v", err)
		}
	})
}
