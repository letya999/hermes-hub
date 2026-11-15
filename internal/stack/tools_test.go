package stack

import (
	"fmt"
	"strings"
	"testing"
)

// The legacy feature vocabulary survives in test fixtures only: these
// tables translate the old `features:` names into the tools/ingress
// selections a published revision would carry.
var featureToolMigration = map[string]ToolEntry{
	"browser":         {Via: ToolViaMCP, Server: "playwright"},
	"web":             {Via: ToolViaNative},
	"deep_research":   {Via: ToolViaNative},
	"hh":              {Via: ToolViaToolHub},
	"ssh":             {Via: ToolViaToolHub},
	"image_gen":       {Via: ToolViaToolHub},
	"meet":            {Via: ToolViaNative},
	"transcription":   {Via: ToolViaNative},
	"google":          {Via: ToolViaMCP},
	"gitlab":          {Via: ToolViaMCP},
	"github":          {Via: ToolViaMCP},
	"slack":           {Via: ToolViaMCP},
	"atlassian":       {Via: ToolViaMCP},
	"telegram_user":   {Via: ToolViaMCP},
	"desktop":         {Via: ToolViaMCP},
	"drafts":          {Via: ToolViaMCP},
	"exposure":        {Via: ToolViaToolHub},
	"hosting":         {Via: ToolViaToolHub},
	"memory_vault":    {Via: ToolViaNative},
	"knowledge_ui":    {Via: ToolViaNative},
	"memory_provider": {Via: ToolViaNative},
}

var featureToggleMigration = map[string][2]string{
	"browser_act":    {"browser", "act"},
	"ssh_write":      {"ssh", "write"},
	"ssh_shell":      {"ssh", "shell"},
	"ssh_tunnel":     {"ssh", "tunnel"},
	"google_write":   {"google", "write"},
	"telegram_write": {"telegram_user", "write"},
}

// featureTools converts legacy feature names into their tools/ingress
// equivalents — the fixture vocabulary the retired migration used to
// provide.
func featureTools(features []string) (map[string]ToolEntry, []string, error) {
	tools := map[string]ToolEntry{}
	var ingress []string
	for _, feature := range features {
		switch {
		case feature == "workspace":
		case feature == "telegram" || feature == "slack_app":
			ingress = append(ingress, feature)
		case featureToolMigration[feature].Via != "":
			tools[feature] = featureToolMigration[feature]
		default:
			pair, ok := featureToggleMigration[feature]
			if !ok {
				return nil, nil, fmt.Errorf("unknown legacy feature %q", feature)
			}
			e := tools[pair[0]]
			if e.Via == "" {
				e = featureToolMigration[pair[0]]
			}
			if e.Tools == nil {
				e.Tools = map[string]bool{}
			}
			e.Tools[pair[1]] = true
			tools[pair[0]] = e
		}
	}
	return tools, ingress, nil
}

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
	tools, _, err := featureTools(features)
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
	_, ingress, err := featureTools(features)
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
		"exposure", "files", "goals", "media", "memory", "models",
		"research", "routines", "sessions", "settings", "sites", "skills",
		"ssh", "terminal", "watches", "web_search",
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
