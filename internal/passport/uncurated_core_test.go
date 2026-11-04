package passport_test

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
)

// R6/ADR-0094: uncurated toolsets (user_mcp/org_mcp — raw MCP members the
// platform never reviewed) never enter core. An operator writing one into
// core gets it demoted to extended rather than compiling a core surface the
// charter forbids.
func TestUncuratedToolsetNeverInCore(t *testing.T) {
	in := fixtureInputs(t)
	mcp := map[string]any{"via": "mcp-raw"}
	in.PlatformCeiling.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"files":       map[string]any{"via": "toolhub"},
		"user_mcp":    mcp,
	}
	in.OrgCeiling.Docs["tools"] = map[string]any{"core": map[string]any{
		"native:file": map[string]any{"via": "native"},
		"files":       map[string]any{"via": "toolhub"},
		"user_mcp":    mcp,
	}}
	in.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"user_mcp":    mcp,
	}
	rev, err := passport.Compile(in)
	if err != nil {
		t.Fatalf("uncurated in core must demote to extended, not fail: %v", err)
	}
	core, err := rev.Value("tools", "core")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := core.(map[string]any)["user_mcp"]; ok {
		t.Fatal("uncurated user_mcp stayed in core")
	}
	extended, err := rev.Value("tools", "extended")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extended.(map[string]any)["user_mcp"]; !ok {
		t.Fatalf("user_mcp lost entirely: extended=%v", extended)
	}
}

// An ambiguous short name — one resolving under more than one executor
// namespace — fails closed rather than binding the wrong executor.
func TestAmbiguousToolsetNameFailsClosed(t *testing.T) {
	docs := catalog.Docs{Toolsets: catalog.ToolsetsDoc{Toolsets: map[string]catalog.Toolset{
		"hub:browser":    {Kind: "family", Section: "hub:browser", Via: []string{"toolhub"}},
		"native:browser": {Kind: "native", Section: "native:browser", Via: []string{"native"}},
	}}}
	if _, err := passport.ResolveToolset(docs, "browser"); err == nil {
		t.Fatal("short name matching hub: and native: must fail as ambiguous")
	}
}

// via picks between executor surfaces of the same name: native admits only
// its own route, a family admits only toolhub.
func TestViaSelectsExecutorSurface(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	ts, err := passport.ResolveToolset(docs, "native:file")
	if err != nil || ts.Kind != "native" {
		t.Fatalf("native:file must resolve the native carveout: %v kind=%q", err, ts.Kind)
	}
	ts, err = passport.ResolveToolset(docs, "files")
	if err != nil || ts.Kind != "family" {
		t.Fatalf("files must resolve the first-party family: %v kind=%q", err, ts.Kind)
	}
}
