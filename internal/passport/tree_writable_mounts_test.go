package passport_test

import (
	"slices"
	"testing"
)

// An rw mount target is mounted writable by the kernel — the emitted
// boundary must admit it even when no layer lists it in writable, just as
// ro mount targets already join read_only.
func TestWorkspaceRWMountTargetJoinsWritable(t *testing.T) {
	ws := typedWorkspace()
	ws["writable"] = []any{"/workspace"}
	ws["mounts"] = map[string]any{
		"/shared/team-docs": map[string]any{"source": "team/docs", "mode": "rw"},
	}
	rev, err := treeWorkspaceCompile(t, ws)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	doc := workspaceDoc(t, rev)
	got := []string{}
	for _, p := range doc["writable"].([]any) {
		got = append(got, p.(string))
	}
	if !slices.Contains(got, "/shared/team-docs") {
		t.Fatalf("writable %v misses rw mount target /shared/team-docs", got)
	}
}
