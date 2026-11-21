package agenttools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// M32/#517: the in-runtime executor serves no memory tool — memory_search
// already executes inside ToolHub (#515) and memory_card's writer moves
// there under #518; until then both report explicit unavailable rather than
// touching a vault the runtime no longer mounts.
func TestMemoryToolsReportUnavailableOnRuntime(t *testing.T) {
	ws, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	tools := &Tools{Workspace: ws}
	for _, name := range []string{"memory_search", "memory_read", "memory_card"} {
		for _, op := range []Input{{Query: "earl grey"}, {Op: "remember", Text: "earl grey supplier"}} {
			if _, err := tools.Call(context.Background(), name, op); err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("%s reached a local path: %v", name, err)
			}
		}
	}
}
