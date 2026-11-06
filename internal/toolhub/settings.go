package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

func settingsBackend(root, environment, supervisorURL, auth string) func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	return func(ctx context.Context, effective api.EffectiveBinding, tool api.ToolSpec, arguments map[string]any) (api.BackendResult, error) {
		principal := effective.Binding.PrincipalID
		if root == "" || !identity.ValidID(principal) || (environment != "dev" && environment != "prod") {
			return api.BackendResult{}, api.ErrIsolation
		}
		genDir := filepath.Join(root, principal)
		if _, err := os.Stat(filepath.Join(genDir, "passports", passportview.ApplyJournalFile)); err == nil || !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- principal is a validated ID under an operator-owned root
			return api.BackendResult{}, api.ErrUnauthorized
		}
		published, err := passportview.LoadEffectiveGenerated(genDir)
		if err != nil || published.Header.Metadata.Space != principal || published.Header.Metadata.Environment != environment {
			return api.BackendResult{}, api.ErrUnauthorized
		}
		key, _ := arguments["key"].(string)
		if key != "" {
			if _, ok := selfsettings.PublishedPath(key); !ok {
				return api.BackendResult{}, api.ErrInvalid
			}
		}
		switch tool.Name {
		case "settings_read":
			body, err := yaml.Marshal(published.Config)
			if err != nil {
				return api.BackendResult{}, err
			}
			var config map[string]any
			if err := yaml.Unmarshal(body, &config); err != nil {
				return api.BackendResult{}, err
			}
			values := map[string]any{}
			for _, candidate := range selfsettings.AllowedKeys() {
				if key != "" && key != candidate {
					continue
				}
				path, _ := selfsettings.PublishedPath(candidate)
				if value, present := publishedSetting(config, path); present {
					values[candidate] = value
				}
			}
			return api.BackendResult{Structured: map[string]any{
				"published_revision":     published.Header.Metadata.Revision,
				"effective_revision":     published.Header.Metadata.Revision,
				"published_values":       values,
				"manageable_keys":        selfsettings.AllowedKeys(),
				"secret_values_included": false,
			}}, nil
		case "settings_edit":
			base, _ := arguments["base_revision"].(string)
			if key == "" || base == "" || supervisorURL == "" || auth == "" {
				return api.BackendResult{}, api.ErrInvalid
			}
			var value *string
			if raw, present := arguments["value"]; present && raw != nil {
				text, ok := raw.(string)
				if !ok {
					return api.BackendResult{}, api.ErrInvalid
				}
				value = &text
			}
			body, err := json.Marshal(map[string]any{"principal_id": principal, "environment": environment, "key": key, "value": value, "base_revision": base})
			if err != nil {
				return api.BackendResult{}, err
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(supervisorURL, "/")+"/v1/settings", bytes.NewReader(body)) // #nosec G704 -- supervisor URL comes from the operator-owned service environment
			if err != nil {
				return api.BackendResult{}, err
			}
			request.Header.Set("Authorization", "Bearer "+auth)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request) // #nosec G704 -- request targets the operator-owned supervisor endpoint
			if err != nil {
				return api.BackendResult{}, api.ErrIsolation
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				var failure map[string]string
				if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) == nil && failure["error"] != "" {
					return api.BackendResult{IsError: true, Text: failure["error"]}, nil
				}
				return api.BackendResult{}, api.ErrUnauthorized
			}
			var result map[string]any
			if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
				return api.BackendResult{}, api.ErrIsolation
			}
			return api.BackendResult{Structured: result}, nil
		default:
			return api.BackendResult{}, api.ErrInvalid
		}
	}
}

func publishedSetting(config map[string]any, path string) (any, bool) {
	var value any = config
	for _, part := range strings.Split(path, ".") {
		section, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = section[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
