package agenttools

import (
	"context"
	"testing"
)

// The direct MCP surface carries the files family that serves
// root=knowledge; no knowledge_* names remain discoverable (SPEC-0093 R3.1).
func TestKnowledgeMCPTransportParity(t *testing.T) {
	client, close := connectHub(t, fixture(t))
	defer close()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, tool := range list.Tools {
		present[tool.Name] = true
	}
	for _, name := range []string{"file_list", "file_read", "file_search"} {
		if !present[name] {
			t.Errorf("%s missing from MCP discovery", name)
		}
	}
	for _, tool := range list.Tools {
		if len(tool.Name) > 9 && tool.Name[:9] == "knowledge" {
			t.Errorf("knowledge tool %s survived on the MCP surface", tool.Name)
		}
	}
}
