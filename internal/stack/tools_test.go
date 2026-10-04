package stack

import (
	"strings"
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

func testNativeTools(names ...string) map[string]ToolEntry {
	return nativeToolsEntries(names)
}
