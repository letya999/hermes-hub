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

func TestHubToolsDefinitionValidatesSandboxed(t *testing.T) {
	d := HubToolsDefinition()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range d.Tools {
		if tool.Sandboxed && tool.Name != "code_exec" {
			t.Fatalf("unexpected sandboxed tool %q", tool.Name)
		}
		if tool.Name == "code_exec" {
			found = true
			if !tool.Sandboxed || len(tool.Uses) != 3 {
				t.Fatalf("code_exec contract wrong: %+v", tool)
			}
		}
	}
	if !found {
		t.Fatal("code_exec missing from the hub catalog")
	}
}
