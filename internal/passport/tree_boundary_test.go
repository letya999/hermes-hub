package passport_test

import (
	"strings"
	"testing"
)

func TestWorkspaceInvariantReadOnlyCannotBecomeWritable(t *testing.T) {
	// Equal path: /vault/memory is an invariant ro root — writable loses.
	ws := typedWorkspace()
	ws["writable"] = append(ws["writable"].([]any), "/vault/memory")
	if _, err := treeWorkspaceCompile(t, ws); err == nil {
		t.Fatal("writable /vault/memory must be rejected")
	} else if !strings.Contains(err.Error(), "read-only") && !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Descendant path: a writable entry under an ro ancestor is dead on
	// arrival — longest-prefix resolution gives read-only the win.
	ws2 := typedWorkspace()
	ws2["writable"] = append(ws2["writable"].([]any), "/config/prompts")
	if _, err := treeWorkspaceCompile(t, ws2); err == nil {
		t.Fatal("writable /config/prompts under read-only /config must be rejected")
	}
}
