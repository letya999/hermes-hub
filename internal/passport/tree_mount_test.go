package passport_test

import (
	"strings"
	"testing"
)

func TestWorkspaceMountSourceOutsideScopeRejected(t *testing.T) {
	cases := []struct {
		name  string
		mount map[string]any
		want  string
	}{
		{"escape", map[string]any{"source": "../../outside", "mode": "ro"}, "unsafe"},
		{"absolute", map[string]any{"source": "/etc/passwd", "mode": "ro"}, "unsafe"},
		{"org-rw", map[string]any{"source": "org:acme/docs", "mode": "rw"}, "read-only"},
		{"outside-shared", map[string]any{"source": "docs", "mode": "ro"}, "/shared/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := typedWorkspace()
			target := "/shared/x"
			if tc.name == "outside-shared" {
				target = "/vault/hidden"
			}
			ws["mounts"] = map[string]any{target: tc.mount}
			_, err := treeWorkspaceCompile(t, ws)
			if err == nil {
				t.Fatalf("mount %v at %s must be rejected", tc.mount, target)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", err, tc.want)
			}
		})
	}
}
