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
	for _, want := range []string{"exposure_list", "exposure_create", "exposure_remove"} {
		if !names[want] {
			t.Fatal("catalog spec missing:", want)
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

func TestHubToolsDefinitionValidatesSandboxed(t *testing.T) {
	d := HubToolsDefinition()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range d.Tools {
		if tool.Sandboxed {
			t.Fatalf("unexpected sandboxed tool %q", tool.Name)
		}
		if tool.Name == "terminal_exec" {
			found = true
			if tool.Sandboxed || len(tool.Uses) != 2 ||
				tool.Uses[0].Action != "exec" || tool.Uses[0].Resource != "terminal" ||
				tool.Uses[1].Action != "read" || tool.Uses[1].Resource != "files" || tool.Uses[1].PathArgument != "path" {
				t.Fatalf("terminal_exec contract wrong: %+v", tool)
			}
		}
	}
	if !found {
		t.Fatal("terminal_exec missing from the hub catalog")
	}
}

// R8.3: terminal_exec's optional `skill` argument is the schema-only lease
// marker — the agent names the skill, the executor resolves the one-call env.
func TestCodeExecDeclaresSkillLeaseArgument(t *testing.T) {
	d := HubToolsDefinition()
	for _, tool := range d.Tools {
		if tool.Name != "terminal_exec" {
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
		t.Fatalf("terminal_exec arguments lack skill: %+v", tool.Arguments)
	}
	t.Fatal("terminal_exec missing from the hub catalog")
}

// R10.6: skill_edit's git actions ride the same tool — url/ref/path/grant
// are optional because the non-git actions still dominate the call shape.
func TestSkillManageDeclaresGitArguments(t *testing.T) {
	d := HubToolsDefinition()
	for _, tool := range d.Tools {
		if tool.Name != "skill_edit" {
			continue
		}
		declared := map[string]bool{}
		for _, a := range tool.Arguments {
			declared[a.Name] = true
		}
		for _, want := range []string{"url", "ref", "path", "grant"} {
			if !declared[want] {
				t.Fatalf("skill_edit arguments lack %q: %+v", want, tool.Arguments)
			}
		}
		if !strings.Contains(tool.Description, "install") {
			t.Fatalf("skill_edit description hides the git actions: %q", tool.Description)
		}
		return
	}
	t.Fatal("skill_edit missing from the hub catalog")
}

// M32/#518: memory_card executes inside ToolHub against the verified
// caller's own vault — the description must say ToolHub, never the retired
// stamped-run wording, and the executor stays runtime:toolhub.
func TestMemoryCardDeclaresToolHubExecutor(t *testing.T) {
	d := HubToolsDefinition()
	for _, tool := range d.Tools {
		if tool.Name != "memory_card" {
			continue
		}
		if tool.Executor != "runtime:toolhub" {
			t.Fatalf("memory_card executor = %q", tool.Executor)
		}
		if !strings.Contains(tool.Description, "ToolHub") || strings.Contains(tool.Description, "stamped private run") {
			t.Fatalf("memory_card description drifted: %q", tool.Description)
		}
		// M32/#519: edits to existing memory are proposal-gated — the
		// contract must name propose, the inner op and the owner's verified
		// confirmation, and the schema must carry the inner argument.
		for _, want := range []string{"propose", "inner", "owner"} {
			if !strings.Contains(tool.Description, want) {
				t.Fatalf("memory_card description missing %q: %q", want, tool.Description)
			}
		}
		var inner bool
		for _, a := range tool.Arguments {
			switch a.Name {
			case "inner":
				inner = true
			case "op":
				// The op enum must not advertise edits the model cannot
				// execute — truth/merge/forget are proposal inner ops now.
				for _, e := range a.Enum {
					if e == "truth" || e == "merge" || e == "forget" {
						t.Fatalf("memory_card op enum advertises a non-executable edit: %+v", a.Enum)
					}
				}
			}
		}
		if !inner {
			t.Fatalf("memory_card schema lacks the inner argument: %+v", tool.Arguments)
		}
		return
	}
	t.Fatal("memory_card missing from the hub catalog")
}
