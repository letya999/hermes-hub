package passport_test

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
)

// Route ladders may only reference declared services — a dangling hop would
// fail closed at runtime, so the passport rejects it at compile.
func TestModelsLadderReferencesKnownServices(t *testing.T) {
	in := fixtureInputs(t)
	routes := in.PlatformCeiling.Docs["models"]["routes"].(map[string]any)
	routes["everyday"].(map[string]any)["ladder"] = []any{"cliproxy", "vanished"}
	if _, err := passport.Compile(in); err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("want unknown service error, got %v", err)
	}
}
