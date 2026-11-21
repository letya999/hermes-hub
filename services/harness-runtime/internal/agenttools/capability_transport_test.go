package agenttools

import (
	"context"
	"strings"
	"testing"
)

func TestRoutineDiscoveryExplainsSameDeterministicContract(t *testing.T) {
	client, close := connectHub(t, fixture(t))
	defer close()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	projected := map[string]string{}
	for _, tool := range HubToolsDefinition().Tools {
		projected[tool.Name] = tool.Description
	}
	for _, tool := range list.Tools {
		if !strings.HasPrefix(tool.Name, "routine_") {
			continue
		}
		if tool.Description != projected[tool.Name] {
			t.Errorf("%s metadata differs across transports", tool.Name)
		}
		if tool.Name == "routine_create" {
			for _, token := range []string{"after:", "baseline", "old/new", "capture", "never wake"} {
				if !strings.Contains(tool.Description, token) {
					t.Errorf("routine_create missing %q", token)
				}
			}
		}
	}
}

// The direct MCP surface carries the catalog's file-family tools too:
// unmanaged runtimes without a ToolHub projection still expose
// project_create, so a missing top-level project can never dead-end.
func TestProjectCreateOnDirectSurface(t *testing.T) {
	client, close := connectHub(t, fixture(t))
	defer close()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, tool := range list.Tools {
		names[tool.Name] = tool.Description
	}
	var want string
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name == "project_create" {
			want = spec.Description
		}
	}
	if got, ok := names["project_create"]; !ok || got != want {
		t.Fatalf("direct MCP surface lacks catalog project_create: %v", names)
	}
}
