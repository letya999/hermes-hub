package selfsettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	current, err := Set(path, "compression.threshold_tokens", "150000")
	if err != nil {
		t.Fatal(err)
	}
	if current["compression.threshold_tokens"] != int64(150000) {
		t.Fatalf("stored %v", current)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["compression.threshold_tokens"] != int64(150000) {
		t.Fatalf("loaded %v", loaded)
	}
	// A second set preserves the first.
	if _, err := Set(path, "compression.in_place", "false"); err != nil {
		t.Fatal(err)
	}
	loaded, _ = Load(path)
	if loaded["compression.in_place"] != false || loaded["compression.threshold_tokens"] != int64(150000) {
		t.Fatalf("overlay lost keys: %v", loaded)
	}
	// nil resets to the rendered default.
	if _, err := Set(path, "compression.in_place", nil); err != nil {
		t.Fatal(err)
	}
	loaded, _ = Load(path)
	if _, present := loaded["compression.in_place"]; present {
		t.Fatal("reset key persisted")
	}
}

func TestSetRejectsUnknownAndOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	for _, tc := range []struct{ key, value string }{
		{"mcp_servers", "http://evil"},
		{"model.default", "other-model"},
		{"terminal.enabled", "true"},
		{"compression.unknown_key", "1"},
		{"compression.threshold_tokens", "5"},
		{"compression.threshold_tokens", "99999999"},
		{"compression.enabled", "notabool"},
		{"image_gen.model", "dall-e-3"},
		{"image_gen.delivery", "telegram"},
		{"display.busy_input_mode", "loud"},
	} {
		if _, err := Set(path, tc.key, tc.value); err == nil {
			t.Fatalf("%s=%s accepted", tc.key, tc.value)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected %s wrote the overlay", tc.key)
		}
	}
}

func TestSetAcceptsReviewedTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	for _, tc := range []struct {
		key   string
		value string
		want  any
	}{
		{"compression.enabled", "false", false},
		{"compression.threshold", "0.4", 0.4},
		{"compression.threshold_tokens", "200000", int64(200000)},
		{"display.busy_input_mode", "interrupt", "interrupt"},
		{"image_gen.model", "gemini-3.1-flash-image", "gemini-3.1-flash-image"},
		{"image_gen.delivery", "url", "url"},
	} {
		if _, err := Set(path, tc.key, tc.value); err != nil {
			t.Fatalf("%s=%s rejected: %v", tc.key, tc.value, err)
		}
	}
	loaded, err := Load(path)
	if err != nil || len(loaded) != 6 {
		t.Fatalf("loaded %v %v", loaded, err)
	}
}

func TestLoadRejectsTamperedOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	for _, body := range []string{
		`{"mcp_servers": {"evil": {}}}`,
		`{"compression.enabled": "yes"}`,
		`{"compression.threshold_tokens": 1}`,
		`{"compression.enabled": true, "egress": {"relay": "x"}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("tampered overlay accepted: %s", body)
		}
	}
	// Non-regular files fail closed too.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("self-settings-target", path); err == nil {
		if _, err := Load(path); err == nil {
			t.Fatal("symlink overlay accepted")
		}
	}
}

func TestMergeAppliesDottedPaths(t *testing.T) {
	config := map[string]any{
		"compression": map[string]any{"enabled": true, "threshold_tokens": 200000},
		"model":       map[string]any{"default": "kept"},
	}
	Merge(config, map[string]any{
		"compression.threshold_tokens": int64(64000),
		"display.busy_input_mode":      "interrupt",
	})
	if config["compression"].(map[string]any)["threshold_tokens"] != int64(64000) {
		t.Fatalf("merge: %v", config)
	}
	if config["compression"].(map[string]any)["enabled"] != true {
		t.Fatal("merge dropped sibling key")
	}
	if config["display"].(map[string]any)["busy_input_mode"] != "interrupt" {
		t.Fatal("merge did not create the nested section")
	}
	if config["model"].(map[string]any)["default"] != "kept" {
		t.Fatal("merge touched unrelated config")
	}
}

func TestMissingOverlayLoadsNil(t *testing.T) {
	loaded, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil || loaded != nil {
		t.Fatalf("missing overlay: %v %v", loaded, err)
	}
}
