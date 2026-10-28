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

func TestExposureToolsInCatalog(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range HubToolsDefinition().Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"exposure_list", "exposure_status", "exposure_open", "exposure_close"} {
		if !names[want] {
			t.Fatal("catalog spec missing:", want)
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

// R8.3: code_exec's optional `skill` argument is the schema-only lease marker
// — the agent names the skill, the executor resolves the one-call env.
func TestCodeExecDeclaresSkillLeaseArgument(t *testing.T) {
	d := HubToolsDefinition()
	for _, tool := range d.Tools {
		if tool.Name != "code_exec" {
			continue
		}
		for _, a := range tool.Arguments {
			if a.Name == "skill" {
				if a.Required {
					t.Fatal("skill lease must be optional")
				}
				return
			}
		}
		t.Fatalf("code_exec arguments lack skill: %+v", tool.Arguments)
	}
	t.Fatal("code_exec missing from the hub catalog")
}
