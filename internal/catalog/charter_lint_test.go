package catalog

import (
	"regexp"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Charter name lint (docs/tool-design.md §3, M31.11 S1): every model-facing
// cataloged tool passes the naming charter mechanically — `<noun>_<verb>`
// snake_case ≤ 40 chars, a verb from the closed dictionary, and the noun
// prefix equal to its family singular. Documented exceptions are the
// multiplexed bare/lifecycle names and the pinned platform layer.

// charterVerbs is the closed verb dictionary frozen by catalog r5.
var charterVerbs = map[string]bool{
	// core verbs — one per effect
	"list": true, "read": true, "search": true, "create": true,
	"edit": true, "remove": true, "exec": true, "inspect": true,
	"status": true, "publish": true,
	// admitted verbs
	"extract": true, "convert": true, "generate": true, "fetch": true,
	"mkdir": true, "write": true, "archive": true, "plan": true,
	"sample": true, "switch": true, "install": true,
	// family-scoped verbs: browser (act/capture/open mirror the connector
	// operation names), ssh (shell/tunnel are channel kinds), media
	// (speak/transcribe are the modality names).
	"act": true, "capture": true, "open": true,
	"shell": true, "tunnel": true,
	"speak": true, "transcribe": true,
}

// charterExceptions are the documented §3.1 names that are not
// `<noun>_<verb>`: multiplexed intent tools carrying an `action` enum and
// the pinned platform layer.
var charterExceptions = map[string]string{
	"skill":              "bare-noun multiplex: skill lifecycle via action enum",
	"terminal_job":       "job lifecycle multiplex via action enum",
	"platform_guide":     "pinned platform layer",
	"platform_status":    "pinned platform layer",
	"platform_bootstrap": "pinned platform layer",
	"platform_usage":     "pinned platform layer",
	"tool_search":        "pinned platform layer",
	"tool_call":          "pinned platform layer",
	"memory_card":        "write-path multiplex via op enum: remember/fact/truth/merge/forget",
	"web_providers":      "provider inventory name preserved during web_search family migration",
	"web_search_multi":   "multi-provider search name preserved during web_search family migration",
	"web_cache":          "cache lifecycle name preserved during web_search family migration",
}

// charterFamilies maps the noun prefix to the catalog family it must equal
// in CapabilityID.
var charterFamilies = map[string]string{
	"file": "files", "document": "documents", "media": "media",
	"skill": "skills", "routine": "routines", "watch": "watches",
	"assignment": "assignments", "goal": "goals", "context": "context",
	"settings": "settings", "ssh": "ssh", "exposure": "exposure",
	"site": "sites", "web": "web_search", "browser": "browser",
	"research": "research", "connector": "connectors", "model": "models",
	"session": "sessions", "terminal": "terminal", "project": "files",
	"platform": "platform", "tool": "platform", "memory": "memory",
}

var charterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func TestCatalogVocabularyLint(t *testing.T) {
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience != api.AudienceModel {
			continue // internal/operator tools are not model vocabulary
		}
		name := spec.Name
		if !charterNamePattern.MatchString(name) {
			t.Errorf("%s: not <noun>_<verb> snake_case", name)
			continue
		}
		if len(name) > 40 {
			t.Errorf("%s: %d chars exceeds the 40-char budget", name, len(name))
		}
		if why, exempt := charterExceptions[name]; exempt {
			if spec.CapabilityID == "" {
				t.Errorf("%s: documented exception %q lost its capability id", name, why)
			}
			continue
		}
		parts := strings.Split(name, "_")
		if len(parts) < 2 {
			t.Errorf("%s: missing <noun>_<verb> split", name)
			continue
		}
		noun, verb := parts[0], parts[len(parts)-1]
		if !charterVerbs[verb] {
			t.Errorf("%s: verb %q outside the closed dictionary", name, verb)
		}
		family, known := charterFamilies[noun]
		if !known {
			t.Errorf("%s: noun %q names no catalog family", name, noun)
			continue
		}
		capFamily := strings.SplitN(spec.CapabilityID, ".", 2)[0]
		if capFamily != family {
			t.Errorf("%s: noun %q implies family %q, capability is %q", name, noun, family, spec.CapabilityID)
		}
	}
}
