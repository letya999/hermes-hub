package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// settingsBackend serves the settings tool family through the spawn/v1
// contract (S7b): spawn-controller owns the read and the edit — the
// effective digest replaces the file revision everywhere the tool schema
// says published_revision. Until the runtime cutover (S7c) the respawn
// that env-carried config needs still goes to the supervisor's route —
// spawn-controller owns the passport, the supervisor owns the container.
func settingsBackend(environment, supervisorURL, auth string, spawn spawnv1.Client) func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	return func(ctx context.Context, effective api.EffectiveBinding, tool api.ToolSpec, arguments map[string]any) (api.BackendResult, error) {
		principal := effective.Binding.PrincipalID
		if !identity.ValidID(principal) || (environment != "dev" && environment != "prod") {
			return api.BackendResult{}, api.ErrIsolation
		}
		if spawn == nil {
			return api.BackendResult{}, api.ErrIsolation
		}
		tenant := spawnv1.Tenant{Org: "local", Owner: principal}
		key, _ := arguments["key"].(string)
		if key != "" {
			if _, ok := selfsettings.PublishedPath(key); !ok {
				return api.BackendResult{}, api.ErrInvalid
			}
		}
		switch tool.Name {
		case "settings_read":
			result, err := spawn.GetSettings(ctx, tenant, principal, key)
			if err != nil {
				return api.BackendResult{IsError: true, Text: errorText(err)}, nil
			}
			return api.BackendResult{Structured: map[string]any{
				"published_revision":     result.PublishedRevision,
				"effective_revision":     result.EffectiveRevision,
				"published_values":       result.PublishedValues,
				"manageable_keys":        result.ManageableKeys,
				"secret_values_included": result.SecretValuesIncluded,
			}}, nil
		case "settings_edit":
			base, _ := arguments["base_revision"].(string)
			if key == "" || base == "" {
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
			result, err := spawn.EditSettings(ctx, principal, spawnv1.SettingsEditRequest{
				Org: tenant.Org, Owner: tenant.Owner, Key: key, Value: value,
				BaseRevision:   base,
				IdempotencyKey: "settings-" + principal + "-" + key,
			})
			if err != nil {
				return api.BackendResult{IsError: true, Text: errorText(err)}, nil
			}
			// Transitional (S7b, removed in S7c): runtimes are still
			// supervisor-owned, so the env-carried config change reaches
			// the container through the supervisor respawn route.
			if result.Changed && supervisorURL != "" {
				if err := respawnSupervised(ctx, client, supervisorURL, auth, principal); err != nil {
					return api.BackendResult{IsError: true, Text: errorText(err)}, nil
				}
			}
			return api.BackendResult{Structured: map[string]any{
				"published_revision": result.PublishedRevision,
				"previous_revision":  result.PreviousRevision,
				"changed":            result.Changed,
			}}, nil
		default:
			return api.BackendResult{}, api.ErrInvalid
		}
	}
}

// respawnSupervised asks the supervisor to replace the principal's
// runtime — the same route hubctl's respawn action uses.
func respawnSupervised(ctx context.Context, client *http.Client, supervisorURL, auth, principal string) error {
	body, _ := json.Marshal(map[string]string{"principal_id": principal, "context_id": principal})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(supervisorURL, "/")+"/v1/respawn", bytes.NewReader(body)) // #nosec G704 -- supervisor URL comes from the operator-owned service environment
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) // #nosec G704 -- request targets the operator-owned supervisor endpoint
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New("supervisor respawn: " + strings.TrimSpace(string(detail)))
	}
	return nil
}

// errorText surfaces the service's refusal text to the tool caller —
// contract errors carry the localized reason the service produced.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
