package supervisor

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

func TestSupervisedHostingMountFollowsSelectedSites(t *testing.T) {
	root := t.TempDir()
	settings := stack.Settings{Tools: map[string]stack.ToolEntry{"sites": {Via: "toolhub"}}}
	if got := appendHostingRuntimeMount(nil, root, "dev", settings); len(got) != 0 {
		t.Fatalf("hosting mounted without a slot: %d args", len(got))
	}
	settings.Hosting.Slots = []string{"m32test"}
	got := appendHostingRuntimeMount(nil, root, "dev", settings)
	want := "type=bind,src=" + filepath.Join(root, "runtime", "hosting.dev.key") + ",dst=/run/hosting.key,readonly"
	if !slices.Contains(got, want) {
		t.Fatal("supervised hosting token mount missing")
	}
}
