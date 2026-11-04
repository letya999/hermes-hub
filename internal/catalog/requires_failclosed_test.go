package catalog

import "testing"

// R2/SPEC-0093: an unmapped member or capability must fail catalog
// generation, never silently inherit a floor right — a fallback grants a
// caller more than the charter assigned.
func TestUnmappedRequiresFailClosed(t *testing.T) {
	if right := RequiredRight("nonexistent.capability"); right != "" {
		t.Fatalf("unmapped capability resolved to %q; fail closed expects empty", right)
	}
	caps := CapabilitiesDoc{Capabilities: map[string]Capability{}}
	toolsets := map[string]Toolset{
		"files": {Members: []string{"file_list"}},
	}
	if err := annotateToolsets(toolsets, caps); err == nil {
		t.Fatal("a member with no capability mapping must fail generation")
	}
}
