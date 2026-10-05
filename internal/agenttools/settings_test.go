package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsGetListsAllowlistWithoutSecrets(t *testing.T) {
	v := fixture(t)
	out, err := v.SettingsGet(Input{})
	if err != nil {
		t.Fatal(err)
	}
	keys, ok := out["manageable_keys"].([]string)
	if !ok || len(keys) == 0 {
		t.Fatalf("manageable_keys: %v", out)
	}
	if out["secret_values_included"] != false {
		t.Fatal("settings must not include secret values")
	}
	for _, key := range keys {
		leaf := key[strings.LastIndex(key, ".")+1:]
		for _, secret := range []string{"token", "secret", "password", "key", "credential"} {
			if leaf == secret || strings.HasSuffix(leaf, "_"+secret) {
				t.Fatalf("allowlist leaks a secret-shaped key: %v", key)
			}
		}
	}
}

func TestSettingsGetReportsSingleKey(t *testing.T) {
	v := fixture(t)
	out, err := v.SettingsGet(Input{Key: "compression.enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if out["key"] != "compression.enabled" || out["overridden"] != false {
		t.Fatalf("single-key view: %v", out)
	}
	if _, err := v.SettingsGet(Input{Key: "model.default"}); err == nil {
		t.Fatal("unmanageable key accepted")
	}
}

func TestSettingsGetProjectsEffectiveConfig(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HUB_STATE", state)
	config := filepath.Join(state, "hermes.yaml")
	body := "compression:\n  enabled: true\n  threshold_tokens: 250000\nmodel:\n  default: gemini\n"
	if err := os.WriteFile(config, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_CONFIG", config)
	v := fixture(t)
	out, err := v.SettingsGet(Input{})
	if err != nil {
		t.Fatal(err)
	}
	effective, ok := out["effective"].(map[string]any)
	if !ok {
		t.Fatalf("effective: %v", out)
	}
	if effective["compression.enabled"] != true || effective["compression.threshold_tokens"] != 250000 {
		t.Fatalf("effective: %v", effective)
	}
	if _, present := effective["model.default"]; present {
		t.Fatal("non-manageable key leaked into effective view")
	}
}

func TestSettingsSetValidatesPersistsAndRestarts(t *testing.T) {
	v := fixture(t)
	restarted := false
	v.Restart = func() error { restarted = true; return nil }
	out, err := v.SettingsSet(Input{Key: "compression.threshold_tokens", Value: "180000"})
	if err != nil {
		t.Fatal(err)
	}
	if out["restart_required"] != true || out["restart_scheduled"] != true || !restarted {
		t.Fatalf("set: %v restarted=%v", out, restarted)
	}
	overlay := out["overrides"].(map[string]any)
	if overlay["compression.threshold_tokens"] != int64(180000) {
		t.Fatalf("overrides: %v", overlay)
	}
	if _, err := os.Stat(filepath.Join(v.StateDir, "self-settings.json")); err != nil {
		t.Fatal("overlay not persisted")
	}
	// Unmanageable and out-of-range values never write the file.
	for _, in := range []Input{
		{Key: "", Value: "1"},
		{Key: "model.default", Value: "x"},
		{Key: "compression.threshold_tokens", Value: "5"},
	} {
		if _, err := v.SettingsSet(in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
	// Empty value resets the override.
	out, err = v.SettingsSet(Input{Key: "compression.threshold_tokens"})
	if err != nil || out["reset"] != true {
		t.Fatal(out, err)
	}
	overlay = out["overrides"].(map[string]any)
	if _, present := overlay["compression.threshold_tokens"]; present {
		t.Fatal("reset left the override in place")
	}
}

func TestSettingsSetReportsMissingRestart(t *testing.T) {
	v := fixture(t)
	v.Restart = nil
	out, err := v.SettingsSet(Input{Key: "compression.in_place", Value: "true"})
	if err == nil || out["restart_scheduled"] != false {
		t.Fatal(out, err)
	}
	// The value is still persisted for the next materialization.
	overlay := out["overrides"].(map[string]any)
	if overlay["compression.in_place"] != true {
		t.Fatalf("overrides: %v", overlay)
	}
}
