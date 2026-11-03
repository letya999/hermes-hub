package passport

import (
	"strings"
	"testing"
)

// M31.9: the per-turn pointer names the pinned platform pair — the catalog
// r5 surface is tool_search + tool_call, and the pinned platform toolset is
// never counted as an operator-selected core member.
func TestPromptPointerNamesR5PlatformPair(t *testing.T) {
	rev := Revision{ID: "r-0007", Docs: map[string][]byte{
		"tools": []byte(`core:
    files: {}
    terminal: {}
    platform: {}
    discovery: {}
extended:
    media: {}
    skills: {}
`),
	}}
	pointer := rev.PromptPointer()
	if !strings.Contains(pointer, "platform=tool_search+tool_call") {
		t.Fatalf("pointer must name the r5 platform pair: %q", pointer)
	}
	if strings.Contains(pointer, "tools_list") || strings.Contains(pointer, "invoke") || strings.Contains(pointer, "discovery=") {
		t.Fatalf("pointer still names the legacy discovery pair: %q", pointer)
	}
	if strings.Contains(pointer, "core=files,terminal,platform") {
		t.Fatalf("pinned platform must not render as an operator-selected member: %q", pointer)
	}
	if !strings.Contains(pointer, "core=files,terminal") || !strings.Contains(pointer, "extended=+2") {
		t.Fatalf("pointer lost core/extended accounting: %q", pointer)
	}
}
