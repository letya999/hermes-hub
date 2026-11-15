package passport_test

import "testing"

// A declared read_only above a skeleton default narrows it legally — the
// writable default is dropped instead of raising a contradiction (ro wins
// at admission).
func TestWorkspaceReadOnlyAboveSkeletonDropsWritableDefault(t *testing.T) {
	ws := typedWorkspace()
	ws["writable"] = []any{"/workspace"}
	ws["read_only"] = append(ws["read_only"].([]any), "/vault")
	rev, err := treeWorkspaceCompile(t, ws)
	if err != nil {
		t.Fatalf("ro /vault must narrow the skeleton default, not fail: %v", err)
	}
	doc := workspaceDoc(t, rev)
	for _, p := range doc["writable"].([]any) {
		if s := p.(string); s == "/vault/library" || s == "/vault/attachments" {
			t.Fatalf("writable %s survives its declared read-only /vault", s)
		}
	}
}
