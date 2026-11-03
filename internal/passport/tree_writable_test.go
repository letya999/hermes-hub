package passport_test

import (
	"slices"
	"testing"
)

// The fixed skeleton mounts /vault/library and /vault/attachments rw — the
// emitted boundary must admit them even when a layer never names them.
func TestWorkspaceWritableDefaultsCoverFixedSkeleton(t *testing.T) {
	ws := typedWorkspace()
	ws["writable"] = []any{"/workspace"}
	rev, err := treeWorkspaceCompile(t, ws)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	doc := workspaceDoc(t, rev)
	got := []string{}
	for _, p := range doc["writable"].([]any) {
		got = append(got, p.(string))
	}
	for _, want := range []string{"/vault/library", "/vault/attachments", "/workspace"} {
		if !slices.Contains(got, want) {
			t.Fatalf("writable %v misses fixed skeleton path %s", got, want)
		}
	}
}
