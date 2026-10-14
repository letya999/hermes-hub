package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func restartRuntime(stateDir string) error {
	body, err := os.ReadFile(filepath.Join(stateDir, "runtime.json"))
	if err != nil {
		return fmt.Errorf("runtime restart unavailable: %w", err)
	}
	var status struct {
		PIDs []int `json:"pids"`
	}
	if err = json.Unmarshal(body, &status); err != nil || len(status.PIDs) == 0 || status.PIDs[0] <= 1 {
		return fmt.Errorf("invalid runtime marker")
	}
	process, err := os.FindProcess(status.PIDs[0])
	if err != nil {
		return fmt.Errorf("runtime supervisor: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "restart.request"), nil, 0600); err != nil {
		return fmt.Errorf("runtime restart request: %w", err)
	}
	if os.Getenv("HUB_DEFER_RUNTIME_RESTART") == "true" {
		return nil
	}
	go signalRuntime(process)
	return nil
}

func (t *Tools) EnvUpdate(r Input) (map[string]any, error) {
	if t.CommunicationURL != "" {
		return nil, errors.New("connector credentials require the protected form; call service_enable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.envLock != nil {
		if err := t.envLock.Lock(); err != nil {
			return nil, err
		}
		defer t.envLock.Unlock()
	}
	keys, err := envstore.Update(filepath.Join(t.StateDir, envstore.FileName), r.Text, os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"updated": keys, "restart_required": true}
	if t.Restart == nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("environment saved; runtime restart unavailable")
	}
	if err = t.Restart(); err != nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("environment saved; restart required: %w", err)
	}
	out["restart_scheduled"] = true
	out["next_step"] = envUpdateNextStep(filepath.Join(t.StateDir, envstore.FileName))
	return out, nil
}

func envUpdateNextStep(path string) string {
	values, err := envstore.Load(path, os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err == nil && values["JIRA_URL"] != "" && values["JIRA_USERNAME"] != "" && values["JIRA_API_TOKEN"] != "" {
		return "После перезапуска проверить доступ к Jira или показать задачи, созданные за последние 2 дня?"
	}
	return "После перезапуска проверить статус подключений или продолжить настройку?"
}

func (t *Tools) ServiceCatalog() (map[string]any, error) {
	if t.ToolHub != nil {
		entries, err := t.ToolHub.Catalog(t.ToolHubAuth)
		if err != nil {
			return nil, err
		}
		services := make([]any, len(entries))
		for i, entry := range entries {
			services[i] = entry
		}
		return map[string]any{"services": services, "source": "toolhub-manifest", "secret_values_included": false}, nil
	}
	self, err := t.selfServices()
	if err != nil {
		return nil, err
	}
	base := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("HUB_FEATURES"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			base[name] = true
		}
	}
	services := make([]map[string]any, 0)
	for _, info := range stack.ServiceCatalog() {
		enabled := base[info.Name] || self[info.Name]
		missing := missingEnv(info.Requires)
		status := "available"
		if !info.SelfService {
			status = "host-managed"
		} else if enabled && len(missing) == 0 {
			status = "ready"
		} else if enabled {
			status = "missing-credentials"
		}
		if os.Getenv("HUB_ORG_SCOPED") == "true" && !base[info.Name] {
			status = "policy-disabled"
		}
		selfService := info.SelfService
		if t.ToolPolicy != nil {
			selfService, status = false, "host-managed"
		}
		services = append(services, map[string]any{"name": info.Name, "description": info.Description, "requires": info.Requires, "depends_on": info.Depends, "self_service": selfService, "status": status, "missing_env": missing})
	}
	return map[string]any{"services": services, "secret_values_included": false}, nil
}

func (t *Tools) ServiceEnable(r Input) (map[string]any, error) {
	if t.ToolHub != nil {
		return t.toolHubServiceEnable(r)
	}
	if t.ToolPolicy != nil {
		return nil, errors.New("legacy service_enable is unavailable in managed mode; use ToolHub discover/prepare_source/confirm/enable")
	}
	name := strings.TrimSpace(r.Service)
	info, ok := stack.ServiceInfoByName(name)
	if !ok {
		return nil, fmt.Errorf("unknown service %q; call service_catalog first", name)
	}
	if !info.SelfService {
		return nil, fmt.Errorf("service %q is host-managed; enable it in workspace.yaml", name)
	}
	if os.Getenv("HUB_ORG_SCOPED") == "true" {
		return nil, fmt.Errorf("service changes are host-managed in organization scope")
	}
	features := map[string]bool{}
	for feature := range mustServiceDependencies(name) {
		features[feature] = true
	}
	features[name] = true
	missing := []string{}
	for feature := range features {
		service, _ := stack.ServiceInfoByName(feature)
		missing = append(missing, missingEnv(service.Requires)...)
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	if len(missing) > 0 {
		out := map[string]any{"service": name, "enabled": false, "missing_env": missing, "input": "protected-form", "secret_values_included": false}
		if t.CommunicationURL != "" {
			if form, formErr := t.credentialForm(context.Background(), name, missing); formErr == nil {
				for key, value := range form {
					out[key] = value
				}
			} else {
				out["form_error"] = "protected credential form unavailable"
			}
		} else {
			out["form_error"] = "configure HUB_COMMUNICATION_CONTROL_URL"
		}
		return out, nil
	}
	for feature := range features {
		if _, _, _, err := stack.ServiceMCPConfig(feature); err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.envLock != nil {
		if err := t.envLock.Lock(); err != nil {
			return nil, err
		}
		defer t.envLock.Unlock()
	}
	current, err := t.selfServices()
	if err != nil {
		return nil, err
	}
	for feature := range features {
		current[feature] = true
	}
	selected := make([]string, 0, len(current))
	for feature := range current {
		selected = append(selected, feature)
	}
	slices.Sort(selected)
	if err := writeSelfServices(filepath.Join(t.StateDir, "self-services.json"), selected); err != nil {
		return nil, err
	}
	out := map[string]any{"service": name, "enabled": true, "enabled_features": selected, "restart_required": true, "secret_values_included": false}
	if t.Restart == nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("service saved; runtime restart unavailable")
	}
	if err := t.Restart(); err != nil {
		out["restart_scheduled"] = false
		return out, fmt.Errorf("service saved; restart required: %w", err)
	}
	out["restart_scheduled"] = true
	return out, nil
}

func (t *Tools) ServiceDisable(r Input) (map[string]any, error) {
	if t.ToolHub == nil {
		return nil, errors.New("service_disable requires the ToolHub manifest catalog")
	}
	name, version, err := t.toolHubManifest(r.Service)
	if err != nil {
		return nil, err
	}
	if err := t.ToolHub.Disable(t.ToolHubAuth, name, version); err != nil {
		return nil, err
	}
	return map[string]any{"service": name, "version": version, "enabled": false, "restart_required": true, "restart_scheduled": false, "secret_values_included": false}, nil
}

func (t *Tools) toolHubServiceEnable(r Input) (map[string]any, error) {
	if t.OrgScoped {
		return nil, fmt.Errorf("service changes are host-managed in organization scope")
	}
	name, version, err := t.toolHubManifest(r.Service)
	if err != nil {
		return nil, err
	}
	binding, err := t.ToolHub.Enable(t.ToolHubAuth, name, version)
	if err != nil {
		entries, _ := t.ToolHub.Catalog(t.ToolHubAuth)
		for _, entry := range entries {
			if entry.Name == name && entry.Version == version && len(entry.MissingCredentials) > 0 {
				return map[string]any{"service": name, "version": version, "enabled": false, "missing_credentials": entry.MissingCredentials, "input": "required_credentials", "next_step": "call required_credentials for the protected form or OAuth URL", "secret_values_included": false}, nil
			}
		}
		return nil, err
	}
	return map[string]any{"service": name, "version": version, "binding_id": binding.ToolBindingID, "projection_revision": binding.ProjectionRevision, "enabled": true, "restart_required": true, "restart_scheduled": false, "secret_values_included": false}, nil
}

func (t *Tools) toolHubManifest(raw string) (string, string, error) {
	name, version, hasVersion := strings.Cut(strings.TrimSpace(raw), "@")
	entries, err := t.ToolHub.Catalog(t.ToolHubAuth)
	if err != nil {
		return "", "", err
	}
	found := []toolhub.CatalogEntry{}
	for _, entry := range entries {
		if entry.Name == name && (!hasVersion || entry.Version == version) {
			found = append(found, entry)
		}
	}
	if len(found) == 0 {
		return "", "", fmt.Errorf("unknown ToolHub manifest %q; call service_catalog first", raw)
	}
	slices.SortFunc(found, func(a, b toolhub.CatalogEntry) int { return strings.Compare(a.Version, b.Version) })
	return found[len(found)-1].Name, found[len(found)-1].Version, nil
}

func missingEnv(keys []string) []string {
	missing := make([]string, 0)
	for _, key := range keys {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func mustServiceDependencies(name string) map[string]bool {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(current string) {
		info, ok := stack.ServiceInfoByName(current)
		if !ok {
			return
		}
		for _, dependency := range info.Depends {
			if !seen[dependency] {
				seen[dependency] = true
				visit(dependency)
			}
		}
	}
	visit(name)
	return seen
}

func (t *Tools) selfServices() (map[string]bool, error) {
	path := filepath.Join(t.StateDir, "self-services.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("self-services must be a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, errors.New("self-services file is too large")
	}
	var state struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("invalid self-services file: %w", err)
	}
	result := map[string]bool{}
	for _, name := range state.Features {
		info, ok := stack.ServiceInfoByName(name)
		if !ok || !info.SelfService {
			return nil, fmt.Errorf("service %q is not self-service", name)
		}
		if result[name] {
			return nil, fmt.Errorf("duplicate service %q", name)
		}
		result[name] = true
	}
	return result, nil
}

func writeSelfServices(path string, features []string) error {
	body, err := json.Marshal(struct {
		Features []string `json:"features"`
	}{Features: features})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".self-services-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if chmodErr := tmp.Chmod(0600); chmodErr != nil {
		_ = tmp.Close()
		return chmodErr
	}
	_, err = tmp.Write(body)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (t *Tools) allowsAction(action string) bool { return !t.OrgScoped || t.OrgActions[action] }
