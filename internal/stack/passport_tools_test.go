package stack

import (
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

// M31.9: the pinned platform family is not an operator toolset — it must
// never become a workspace.yaml ToolEntry, same as the legacy discovery pin
// it replaced.
func TestPassportToolsSkipsPinnedPlatform(t *testing.T) {
	tools := passportTools(spawnv1.Tools{
		Core: map[string]spawnv1.Toolset{
			"platform":  {Via: "toolhub"},
			"discovery": {Via: "toolhub"}, // pre-r5 passports
			"files":     {Via: "toolhub"},
		},
	})
	if tools["platform"].Via != "" || tools["discovery"].Via != "" {
		t.Fatalf("pinned/meta toolsets leaked into the tools map: %v", tools)
	}
	if tools["files"].Via != ToolViaToolHub {
		t.Fatalf("admitted toolset lost: %v", tools)
	}
}

func TestPassportToolsNativeWebAndToolHubWebStable(t *testing.T) {
	declared := spawnv1.Tools{Extended: map[string]spawnv1.Toolset{
		"native:web": {Via: "native"},
		"web":        {Via: "toolhub"},
	}}
	for i := 0; i < 100; i++ {
		got := passportTools(declared)
		if got["web"].Via != ToolViaToolHub {
			t.Fatalf("iteration %d: web backend changed: %+v", i, got["web"])
		}
	}
}
