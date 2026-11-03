package stack

import (
	"strings"
	"testing"
)

// testTools builds a tools map from legacy feature names or explicit
// `name=backend` pairs, so fixtures stay terse after the schema-2 switch.
func testTools(args ...string) map[string]ToolEntry {
	out := map[string]ToolEntry{}
	var features []string
	for _, arg := range args {
		if name, backend, ok := strings.Cut(arg, "="); ok {
			out[name] = ToolEntry{Via: backend}
			continue
		}
		features = append(features, arg)
	}
	tools, _, err := LegacyFeatureTools(features)
	if err != nil {
		panic(err)
	}
	for name, entry := range tools {
		out[name] = entry
	}
	return out
}

// testIngress extracts the ingress half of the same legacy feature list.
func testIngress(args ...string) []string {
	var features []string
	for _, arg := range args {
		if !strings.Contains(arg, "=") {
			features = append(features, arg)
		}
	}
	_, ingress, err := LegacyFeatureTools(features)
	if err != nil {
		panic(err)
	}
	return ingress
}

// M31: every placeable catalog r5 family is a valid `via: toolhub` wish —
// the operator vocabulary cannot lag the frozen toolset list. The pinned
// `platform` layer and the retired `discovery` pseudo-toolset are never
// selectable.
func TestEveryR5FamilyAcceptsToolhubVia(t *testing.T) {
	// via: toolhub toolsets declared by platform/catalogs/6/toolsets.yaml,
	// minus the pinned platform layer.
	families := []string{
		"assignments", "browser", "connectors", "context", "documents",
		"exposure", "files", "goals", "media", "models", "research",
		"routines", "sessions", "settings", "sites", "skills", "ssh",
		"terminal", "watches", "web",
	}
	for _, family := range families {
		s := validSettings()
		s.Tools = map[string]ToolEntry{family: {Via: ToolViaToolHub}}
		if err := s.Validate(); err != nil {
			t.Errorf("tools.%s via toolhub rejected: %v", family, err)
		}
		if got := ToolHubCapabilityID(family); got != family {
			t.Errorf("ToolHubCapabilityID(%q) = %q, want the r5 family id", family, got)
		}
	}
	for _, name := range []string{"platform", "discovery"} {
		s := validSettings()
		s.Tools = map[string]ToolEntry{name: {Via: ToolViaToolHub}}
		if err := s.Validate(); err == nil {
			t.Errorf("tools.%s via toolhub must be refused — the layer is pinned, not selectable", name)
		}
	}
}

func testNativeTools(names ...string) map[string]ToolEntry {
	return nativeToolsEntries(names)
}
