package selfsettings

import (
	"slices"
	"testing"
)

func TestPublishedSettingsValidation(t *testing.T) {
	for _, test := range []struct {
		key   string
		value any
		want  any
	}{
		{"compression.enabled", "true", true},
		{"compression.threshold", "0.5", 0.5},
		{"compression.threshold_tokens", "180000", int64(180000)},
		{"features.compact.engine", "hub", "hub"},
		{"goals.judge", "aux", "aux"},
		{"image_gen.delivery", "url", "url"},
	} {
		got, err := ValidatePath(test.key, test.value)
		if err != nil || got != test.want {
			t.Fatalf("%s: %v %v", test.key, got, err)
		}
	}
	for _, test := range []struct {
		key   string
		value any
	}{
		{"model.default", "x"},
		{"display.busy_input_mode", "interrupt"},
		{"compression.enabled", "maybe"},
		{"compression.enabled", 1},
		{"compression.threshold", "NaN"},
		{"compression.threshold", "0.01"},
		{"compression.threshold_tokens", "1.5"},
		{"compression.threshold_tokens", "1"},
		{"goals.engine", "unknown"},
		{"image_gen.model", "secret"},
	} {
		if _, err := ValidatePath(test.key, test.value); err == nil {
			t.Fatalf("%s accepted %v", test.key, test.value)
		}
	}
}

func TestPublishedPathsCoverManageableKeys(t *testing.T) {
	keys := AllowedKeys()
	if !slices.IsSorted(keys) || len(keys) == 0 {
		t.Fatalf("manageable keys: %v", keys)
	}
	for _, key := range keys {
		path, ok := PublishedPath(key)
		if !ok || path == "" {
			t.Fatalf("%s has no passport path", key)
		}
	}
	for key, want := range map[string]string{
		"compression.enabled":     "runtime.compression.enabled",
		"goals.max_turns":         "runtime.goals.max_turns",
		"features.compact.engine": "runtime.context.compaction",
		"image_gen.delivery":      "image_gen.delivery",
	} {
		got, _ := PublishedPath(key)
		if got != want {
			t.Fatalf("%s maps to %s, want %s", key, got, want)
		}
	}
	if _, ok := PublishedPath("display.busy_input_mode"); ok {
		t.Fatal("retired display overlay remained manageable")
	}
}
