package agenttools

import "testing"

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
