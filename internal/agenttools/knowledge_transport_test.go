package agenttools

import (
	"context"
	"testing"
)

func TestKnowledgeMCPTransportParity(t *testing.T) {
	client, close := connectHub(t, fixture(t))
	defer close()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"knowledge_list", "knowledge_read", "knowledge_search"} {
		found := false
		for _, tool := range list.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from MCP discovery", name)
		}
	}
}
