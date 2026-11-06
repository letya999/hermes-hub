// Package selfsettings validates the reviewed config passport leaves exposed
// by the settings tool family.
package selfsettings

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// validator parses a tool-supplied string into the canonical type the
// effective config carries.
type validator func(raw any) (any, error)

// keys is the reviewed allowlist. Every entry maps to one config passport
// leaf and cannot widen toolsets, egress, credentials or identity.
var keys = map[string]validator{
	"compression.enabled":                               boolValue,
	"compression.threshold":                             floatRange(0.10, 0.95),
	"compression.threshold_tokens":                      intRange(32_768, 2_097_152),
	"compression.target_ratio":                          floatRange(0.05, 0.80),
	"compression.in_place":                              boolValue,
	"compression.micro_compact":                         boolValue,
	"compression.abort_on_summary_failure":              boolValue,
	"compression.checkpoint_required":                   boolValue,
	"compression.idle_compact_after_seconds":            intRange(0, 86_400),
	"compression.max_attempts":                          intRange(1, 10),
	"compression.protect_last_n":                        intRange(0, 100),
	"compression.protect_first_n":                       intRange(0, 50),
	"compression.proactive_prune_tokens":                intRange(0, 1_048_576),
	"compression.proactive_prune_min_result_chars":      intRange(0, 1_048_576),
	"compression.proactive_prune_min_reclaim_tokens":    intRange(0, 1_048_576),
	"compression.micro_compact_every_n_turns":           intRange(1, 1000),
	"compression.micro_compact_defrag_threshold_tokens": intRange(0, 2_097_152),
	// Context control (SPEC-0072/0074): engine selection stays a bounded
	// choice — it can only reorder reviewed engines, never open a new
	// backend — and sweep/judge bounds cannot exceed the reviewed ceilings.
	"features.compact.engine":                choice("auto", "hub", "native"),
	"goals.engine":                           choice("auto", "hub", "native"),
	"goals.judge":                            choice("auto", "aux", "main", "self"),
	"goals.max_turns":                        intRange(1, 50),
	"compression.hub_sweep.enabled":          boolValue,
	"compression.hub_sweep.threshold_tokens": intRange(32_768, 2_097_152),
	"compression.hub_sweep.cooldown_seconds": intRange(0, 86_400),
	"compression.hub_sweep.precompute":       boolValue,
	"image_gen.model":                        choice(imageModels...),
	"image_gen.delivery":                     choice("workspace", "url"),
}

// imageModels is the model list the reviewed cliproxy provider can actually
// serve — both the images-endpoint and chat-completions families the upstream
// image_gen tool supports. Provider itself is not a knob: it stays pinned by
// the operator's settings.yaml grant.
var imageModels = []string{
	"gpt-image-1.5",
	"gpt-image-2",
	"grok-imagine-image",
	"grok-imagine-image-quality",
	"grok-imagine-image-2.0",
	"gemini-2.5-flash-image",
	"gemini-3-pro-image",
	"gemini-3-pro-image-preview",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-image-preview",
}

// AllowedKeys returns the sorted reviewed key set for tool output and docs.
func AllowedKeys() []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func boolValue(raw any) (any, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("expected true or false, got %q", v)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("expected a boolean, got %v", raw)
}

func floatRange(min, max float64) validator {
	return func(raw any) (any, error) {
		var value float64
		switch v := raw.(type) {
		case float64:
			value = v
		case int:
			value = float64(v)
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("expected a number, got %q", v)
			}
			value = parsed
		default:
			return nil, fmt.Errorf("expected a number, got %v", raw)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
			return nil, fmt.Errorf("value %v outside allowed range %v..%v", value, min, max)
		}
		return value, nil
	}
}

func intRange(min, max int64) validator {
	return func(raw any) (any, error) {
		var value int64
		switch v := raw.(type) {
		case float64:
			if v != math.Trunc(v) {
				return nil, fmt.Errorf("expected an integer, got %v", v)
			}
			value = int64(v)
		case int:
			value = int64(v)
		case string:
			parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("expected an integer, got %q", v)
			}
			value = parsed
		default:
			return nil, fmt.Errorf("expected an integer, got %v", raw)
		}
		if value < min || value > max {
			return nil, fmt.Errorf("value %d outside allowed range %d..%d", value, min, max)
		}
		return value, nil
	}
}

func choice(items ...string) validator {
	return func(raw any) (any, error) {
		value, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string, got %v", raw)
		}
		value = strings.TrimSpace(value)
		if slices.Contains(items, value) {
			return value, nil
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(items, ", "))
	}
}

// ValidatePath exposes one allowlist check for declared-time consumers
// (settings.yaml context blocks): same named error, same canonical value.
func ValidatePath(path string, raw any) (any, error) {
	check, ok := keys[path]
	if !ok {
		return nil, fmt.Errorf("setting %q is not manageable", path)
	}
	return check(raw)
}

// PublishedPath maps a reviewed tool setting to its config passport leaf.
// Display knobs are absent because the config passport has no such fields.
func PublishedPath(key string) (string, bool) {
	if _, ok := keys[key]; !ok {
		return "", false
	}
	switch {
	case strings.HasPrefix(key, "compression."), strings.HasPrefix(key, "goals."):
		return "runtime." + key, true
	case key == "features.compact.engine":
		return "runtime.context.compaction", true
	case strings.HasPrefix(key, "image_gen."):
		return key, true
	}
	return "", false
}
