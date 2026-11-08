package agenttools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeSettingsGetDenied(t *testing.T) {
	v := fixture(t)
	if _, err := v.SettingsGet(Input{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("direct runtime settings read: %v", err)
	}
}

func TestRuntimeSettingsSetDenied(t *testing.T) {
	v := fixture(t)
	v.StateDir = t.TempDir()
	restarted := false
	v.Restart = func() error { restarted = true; return nil }
	if _, err := v.SettingsSet(Input{Key: "compression.enabled", Value: "true"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("direct runtime settings edit: %v", err)
	}
	if restarted {
		t.Fatal("direct runtime settings edit scheduled restart")
	}
	if _, err := os.Stat(filepath.Join(v.StateDir, "self-settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("direct runtime settings edit wrote overlay: %v", err)
	}
}

func TestSettingsCatalogPublishesRevisionGuardedControlTools(t *testing.T) {
	definition := HubToolsDefinition()
	if definition.Version != "18.0.2" {
		t.Fatalf("settings catalog version %s", definition.Version)
	}
	for _, tool := range definition.Tools {
		if tool.Name != "settings_edit" {
			continue
		}
		if tool.Executor != "handler:settings" || len(tool.Requires) != 1 || tool.Requires[0] != "config.edit_own" {
			t.Fatalf("settings edit authority: %+v", tool)
		}
		for _, arg := range tool.Arguments {
			if arg.Name == "base_revision" && arg.Required {
				return
			}
		}
		t.Fatal("settings edit lacks required base_revision")
	}
	t.Fatal("settings_edit missing from catalog")
}
