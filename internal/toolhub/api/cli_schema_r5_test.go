package api

import "testing"

// tool-design §4.2/§4.3 (M31.2): the model-facing schema carries per-argument
// descriptions, enums on closed vocabularies, item types on arrays and a
// closed object boundary — opaque JSON blobs are not a schema.
func TestCliInputSchemaCarriesContractMetadata(t *testing.T) {
	spec := ToolSpec{Name: "demo", Arguments: []CLIArgument{
		{Name: "action", Type: "string", Required: true, Description: "lifecycle verb", Enum: []string{"pause", "resume"}},
		{Name: "paths", Type: "array", Description: "files to act on", Items: "string"},
		{Name: "patch", Type: "object", Description: "fields to update"},
	}}
	schema := CliInputSchema(spec)
	if schema["additionalProperties"] != false {
		t.Fatalf("schema must close the object boundary: %v", schema["additionalProperties"])
	}
	props := schema["properties"].(map[string]any)
	action := props["action"].(map[string]any)
	if action["description"] != "lifecycle verb" {
		t.Errorf("action lost its description: %v", action)
	}
	enum, ok := action["enum"].([]string)
	if !ok || len(enum) != 2 {
		t.Errorf("action lost its enum: %v", action["enum"])
	}
	paths := props["paths"].(map[string]any)
	if items, ok := paths["items"].(map[string]any); !ok || items["type"] != "string" {
		t.Errorf("array argument lost its item type: %v", paths)
	}
	if _, ok := props["patch"].(map[string]any)["properties"]; ok {
		t.Error("free-form patch must not invent properties")
	}
}

// A zero-argument tool still closes the boundary — callers sending junk keys
// get a schema rejection rather than a silently ignored parameter.
func TestCliInputSchemaZeroArgsClosesBoundary(t *testing.T) {
	schema := CliInputSchema(ToolSpec{Name: "empty"})
	if schema["additionalProperties"] != false {
		t.Fatalf("zero-arg schema must close the boundary: %v", schema["additionalProperties"])
	}
}
