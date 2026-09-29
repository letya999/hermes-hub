package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

var runtimeEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ToolHub source review/build runs in an isolated builder and may outlive the
// ordinary MCP call timeout. The user gets Hermes' heartbeat while this call
// is in flight, so do not kill it after the default two minutes.
const toolHubCallTimeoutSeconds = 1800

// MaterializeOptions carries the host-side inputs the in-container startup
// normally reads from the environment when it materializes the effective
// Hermes config.
type MaterializeOptions struct {
	ToolHubEndpoint    string
	ToolHubTokenEnv    string
	RuntimeAuthPresent bool
	ToolHubReconnect   bool
	SelfServicesPath   string
}

// MaterializeHermesConfig renders the effective Hermes config on the host so
// the runtime container can mount it read-only: the agent must not be able to
// add mcp_servers entries inside its writable state directory. The result is
// identical to what the in-container startup produces from the same inputs.
func MaterializeHermesConfig(sourcePath, destPath string, opts MaterializeOptions) error {
	body, err := os.ReadFile(sourcePath) // #nosec G304 -- operator-owned source path
	if err != nil {
		return fmt.Errorf("read Hermes source config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0770); err != nil {
		return err
	}
	if err := os.WriteFile(destPath, body, 0660); err != nil {
		return err
	}
	if err := ApplyHermesConfig(destPath, opts); err != nil {
		return err
	}
	// The rendered file is bind-mounted into the runtime container: on Linux
	// hosts the file keeps the host uid, so it must be world-readable for the
	// container's agent uid (10001). Contents hold endpoint names, no secrets.
	return os.Chmod(destPath, 0644)
}

// ApplyHermesConfig rewrites the effective config in place: self-service MCP
// entries and the ToolHub control entry are merged onto whatever is already
// there. Used in-container when no source config mount exists.
func ApplyHermesConfig(configPath string, opts MaterializeOptions) error {
	if err := applySelfServicesFrom(configPath, opts.SelfServicesPath); err != nil {
		return err
	}
	return applyToolHubConfigWith(configPath, opts)
}

// SelfServiceState is the operator-edited feature list persisted beside the
// runtime state.
type SelfServiceState struct {
	Features []string `json:"features"`
}

// ReadSelfServicesFeatures returns the validated self-service feature names
// from the state file, or nil when it does not exist.
func ReadSelfServicesFeatures(path string) ([]string, error) {
	info, err := os.Lstat(path) // #nosec G304 -- operator-owned state path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("self-services must be a regular file")
	}
	body, err := os.ReadFile(path) // #nosec G304 -- same validated path
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, errors.New("self-services file is too large")
	}
	var current SelfServiceState
	if err := json.Unmarshal(body, &current); err != nil {
		return nil, fmt.Errorf("invalid self-services file: %w", err)
	}
	seen := map[string]bool{}
	for _, name := range current.Features {
		info, ok := ServiceInfoByName(name)
		if !ok || !info.SelfService {
			return nil, fmt.Errorf("service %q is not self-service", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate service %q", name)
		}
		seen[name] = true
	}
	slices.Sort(current.Features)
	return current.Features, nil
}

func applySelfServicesFrom(configPath, servicesPath string) error {
	features, err := ReadSelfServicesFeatures(servicesPath)
	if err != nil || len(features) == 0 {
		return err
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- materialized path
	if err != nil {
		return err
	}
	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("read Hermes config: %w", err)
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
		config["mcp_servers"] = servers
	}
	for _, feature := range features {
		name, server, ok, err := ServiceMCPConfig(feature)
		if err != nil {
			return err
		}
		if ok && name != "" {
			servers[name] = server
		}
	}
	updated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("write Hermes config: %w", err)
	}
	return os.WriteFile(configPath, updated, 0660)
}

func applyToolHubConfigWith(configPath string, opts MaterializeOptions) error {
	endpoint := opts.ToolHubEndpoint
	if endpoint == "" {
		return nil
	}
	if err := toolhub.ValidateBackendEndpoint(endpoint); err != nil {
		return fmt.Errorf("HUB_TOOLHUB_ENDPOINT: %w", err)
	}
	tokenEnv := opts.ToolHubTokenEnv
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	if !runtimeEnvName.MatchString(tokenEnv) {
		return fmt.Errorf("HUB_TOOLHUB_TOKEN_ENV: invalid environment name")
	}
	if !opts.RuntimeAuthPresent {
		return fmt.Errorf("HUB_TOOLHUB_TOKEN_ENV %s is empty", tokenEnv)
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- materialized path
	if err != nil {
		return err
	}
	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("read Hermes config: %w", err)
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
		config["mcp_servers"] = servers
	}
	if existing, ok := servers["toolhub"]; ok {
		server, ok := existing.(map[string]any)
		if !ok || server["url"] != endpoint {
			return fmt.Errorf("mcp_servers.toolhub conflicts with HUB_TOOLHUB_ENDPOINT")
		}
	}
	servers["toolhub"] = map[string]any{
		"url":            endpoint,
		"timeout":        toolHubCallTimeoutSeconds,
		"skip_preflight": true,
		"headers":        map[string]string{"Authorization": "Bearer ${" + tokenEnv + "}"},
	}
	if opts.ToolHubReconnect {
		approvals, ok := config["approvals"].(map[string]any)
		if !ok {
			approvals = map[string]any{}
			config["approvals"] = approvals
		}
		if _, exists := approvals["mcp_reload_confirm"]; !exists {
			approvals["mcp_reload_confirm"] = false
		}
	}
	updated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("write Hermes config: %w", err)
	}
	return os.WriteFile(configPath, updated, 0660)
}
