package api

import (
	"encoding/json"
	"testing"
)

// S4.6: the pinned tool-surface digest is order-independent and reacts to
// any name, schema or effect change.
func TestToolDigestStableAndOrderIndependent(t *testing.T) {
	schemaA := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	tools := []ToolSpec{
		{Name: "search", InputSchema: schemaA, Effect: ReadEffect},
		{Name: "write", InputSchema: schemaA, Effect: WriteEffect},
	}
	shuffled := []ToolSpec{tools[1], tools[0]}
	if ToolSurfaceDigest(tools) != ToolSurfaceDigest(shuffled) {
		t.Fatal("digest is order-dependent")
	}
	changed := []ToolSpec{{Name: "search", InputSchema: schemaA, Effect: ReadEffect}, {Name: "write", InputSchema: schemaA, Effect: ReadEffect}}
	if ToolSurfaceDigest(tools) == ToolSurfaceDigest(changed) {
		t.Fatal("effect change did not move the digest")
	}
	schemaChanged := []ToolSpec{tools[0], {Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`), Effect: WriteEffect}}
	if ToolSurfaceDigest(tools) == ToolSurfaceDigest(schemaChanged) {
		t.Fatal("schema change did not move the digest")
	}
}
