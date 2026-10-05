// Package selfsettings persists the runtime-owned settings overlay: a bounded
// set of reviewed Hermes config knobs the owning user may tune through the
// audited settings tool family. The supervisor merges the file into the
// effective Hermes config at spawn-time materialization and the runtime
// attestation merges the identical view, so an overlay value cannot widen the
// reviewed capability surface and a rejected write never lands half-applied.
package selfsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// FileName is the overlay file inside the runtime state dir.
	FileName  = "self-settings.json"
	maxBytes  = 64 * 1024
	maxValues = 32
)

// Value is one validated scalar override for a reviewed config path.
type Value = any

// validator parses a tool-supplied string into the canonical type the
// effective config carries, or re-validates an already-typed overlay value.
type validator func(raw any) (any, error)

// keys is the reviewed allowlist. Every entry is a non-authority knob: tuning
// the compressor cannot widen toolsets, egress, credentials or identity, so
// the set can only grow through code review, never through the overlay.
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
	"display.busy_input_mode":                           choice("queue", "interrupt"),
	"display.long_running_notifications":                boolValue,
	"image_gen.model":                                   choice(imageModels...),
	"image_gen.delivery":                                choice("workspace", "url"),
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

// Load reads and re-validates the overlay. A hand-edited or corrupt file
// fails closed the same way a rejected tool write does.
func Load(path string) (map[string]any, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("self-settings must be a regular file")
	}
	body, err := os.ReadFile(path) // #nosec G304 -- overlay path is operator-controlled
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errors.New("self-settings file is too large")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid self-settings file: %w", err)
	}
	if len(raw) > maxValues {
		return nil, errors.New("too many self-settings keys")
	}
	values := make(map[string]any, len(raw))
	for key, value := range raw {
		check, ok := keys[key]
		if !ok {
			return nil, fmt.Errorf("setting %q is not manageable", key)
		}
		canonical, err := check(value)
		if err != nil {
			return nil, fmt.Errorf("setting %s: %w", key, err)
		}
		values[key] = canonical
	}
	return values, nil
}

// Set validates and atomically writes one overlay entry. A nil value removes
// the key, restoring the rendered default.
func Set(path, key string, raw any) (map[string]any, error) {
	check, ok := keys[key]
	if !ok {
		return nil, fmt.Errorf("setting %q is not manageable (allowed: %s)", key, strings.Join(AllowedKeys(), ", "))
	}
	current, err := Load(path)
	if err != nil {
		return nil, err
	}
	if current == nil {
		current = map[string]any{}
	}
	if raw == nil {
		delete(current, key)
	} else {
		canonical, err := check(raw)
		if err != nil {
			return nil, fmt.Errorf("setting %s: %w", key, err)
		}
		current[key] = canonical
	}
	if err := writeAtomic(path, current); err != nil {
		return nil, err
	}
	return current, nil
}

// Merge applies overlay values onto a decoded config map in place. Only the
// allowlisted paths exist in values; each maps to a scalar config field.
func Merge(config map[string]any, values map[string]any) {
	for key, value := range values {
		parts := strings.Split(key, ".")
		node := config
		for _, part := range parts[:len(parts)-1] {
			next, ok := node[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				node[part] = next
			}
			node = next
		}
		node[parts[len(parts)-1]] = value
	}
}

func writeAtomic(path string, values map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".self-settings-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
