package agenttools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/selfsettings"
	"gopkg.in/yaml.v3"
)

// settingsPath is the runtime-owned overlay inside the state dir. It is not a
// workspace path: scopes cannot reach it, and the file is merged into the
// effective config only through the reviewed allowlist at materialization.
func (t *Tools) settingsPath() string {
	return filepath.Join(t.StateDir, selfsettings.FileName)
}

// SettingsGet reports the reviewed key allowlist, the persisted overrides and,
// when a mounted effective config exists, the value actually in force. Values
// are scalars from the allowlist — no secret material exists under these keys.
func (t *Tools) SettingsGet(r Input) (map[string]any, error) {
	overrides, err := selfsettings.Load(t.settingsPath())
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"manageable_keys":        selfsettings.AllowedKeys(),
		"overrides":              overrides,
		"secret_values_included": false,
	}
	if key := strings.TrimSpace(r.Key); key != "" {
		value, present := overrides[key]
		out["key"], out["override"], out["overridden"] = key, value, present
		if !slicesContainsStr(selfsettings.AllowedKeys(), key) {
			return nil, fmt.Errorf("setting %q is not manageable", key)
		}
	}
	if effective := t.effectiveSettings(); effective != nil {
		out["effective"] = effective
	}
	return out, nil
}

// effectiveSettings reads the mounted effective Hermes config (read-only) and
// projects every allowlisted path to the value the running config carries —
// including rendered defaults for keys with no override.
func (t *Tools) effectiveSettings() map[string]any {
	configPath := strings.TrimSpace(os.Getenv("HUB_HERMES_CONFIG"))
	if configPath == "" {
		configPath = "/state/hermes/config.yaml"
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- the mounted effective config path.
	if err != nil {
		return nil
	}
	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil
	}
	effective := map[string]any{}
	for _, key := range selfsettings.AllowedKeys() {
		var cur any = config
		found := true
		for _, part := range strings.Split(key, ".") {
			node, ok := cur.(map[string]any)
			if !ok {
				found = false
				break
			}
			if cur, found = node[part]; !found {
				break
			}
		}
		if found {
			effective[key] = cur
		}
	}
	return effective
}

// SettingsSet validates and persists one overlay entry, then schedules the
// runtime restart that materializes it — the same durable discipline as
// service_enable. An empty value removes the override.
func (t *Tools) SettingsSet(r Input) (map[string]any, error) {
	key := strings.TrimSpace(r.Key)
	if key == "" {
		return nil, errors.New("settings_set requires key")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.envLock != nil {
		if err := t.envLock.Lock(); err != nil {
			return nil, err
		}
		defer t.envLock.Unlock()
	}
	var raw any = r.Value
	if strings.TrimSpace(r.Value) == "" {
		raw = nil // reset to the rendered default
	}
	current, err := selfsettings.Set(t.settingsPath(), key, raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"key":                    key,
		"overrides":              current,
		"restart_required":       true,
		"secret_values_included": false,
	}
	if raw == nil {
		out["reset"] = true
	}
	if t.Restart == nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("setting saved; runtime restart unavailable")
	}
	if err = t.Restart(); err != nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("setting saved; restart required: %w", err)
	}
	out["restart_scheduled"] = true
	return out, nil
}

func slicesContainsStr(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
