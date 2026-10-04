package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	Managed            bool
	ToolHubEndpoint    string
	ToolHubTokenEnv    string
	RuntimeAuthPresent bool
	ToolHubReconnect   bool
	SelfServicesPath   string
	// NativeToolsets is the operator-approved carve-out list rendered into
	// this runtime's denylist (settings.yaml `tools:` entries with
	// `via: native`, or HUB_NATIVE_TOOLSETS inside the container).
	NativeToolsets []string
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
	if opts.Managed {
		// Publish a validated revision atomically. A rejected edit must not
		// leave half-written effective YAML for a later runtime restart.
		tmp, err := os.CreateTemp(filepath.Dir(destPath), ".hermes-managed-*")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(name, body, 0600); err != nil {
			return err
		}
		if err := ApplyHermesConfig(name, opts); err != nil {
			return err
		}
		if err := os.Chmod(name, 0644); err != nil {
			return err
		}
		return os.Rename(name, destPath)
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
	if opts.Managed {
		if opts.ToolHubEndpoint == "" || !opts.RuntimeAuthPresent {
			return fmt.Errorf("managed capabilities require a scoped ToolHub endpoint and token")
		}
		if err := ValidateNativeToolsets(opts.NativeToolsets); err != nil {
			return fmt.Errorf("native_toolsets: %w", err)
		}
		if err := validateManagedSourceConfig(configPath, opts.NativeToolsets); err != nil {
			return err
		}
	} else {
		if err := applySelfServicesFrom(configPath, opts.SelfServicesPath); err != nil {
			return err
		}
	}
	return applyToolHubConfigWith(configPath, opts)
}

// The selected model and timezone may vary, but capability-bearing fields
// must exactly match the generated zero template. Extra YAML is denied.
func validateManagedSourceConfig(configPath string, nativeToolsets []string) error {
	body, err := os.ReadFile(configPath) // #nosec G304 -- host-owned materialization path.
	if err != nil {
		return err
	}
	var actual M
	if err := yaml.Unmarshal(body, &actual); err != nil {
		return err
	}
	model, ok := actual["model"].(M)
	if !ok {
		return fmt.Errorf("invalid managed model configuration")
	}
	name, nameOK := model["default"].(string)
	url, urlOK := model["base_url"].(string)
	zone, zoneOK := actual["timezone"].(string)
	if !nameOK || !urlOK || !zoneOK {
		return fmt.Errorf("invalid managed model identity")
	}
	expected, err := yaml.Marshal(managedHermesConfig(Settings{Model: name, ModelURL: url, Timezone: zone, Tools: nativeToolsEntries(nativeToolsets)}))
	if err != nil {
		return err
	}
	var normalized M
	if err := yaml.Unmarshal(expected, &normalized); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, normalized) {
		return fmt.Errorf("managed Hermes source differs from the reviewed zero-capability template")
	}
	return nil
}

// ValidateManagedEffectiveConfig prevents Hermes from falling back to its
// defaults when a mounted managed config is missing, malformed or changed.
func ValidateManagedEffectiveConfig(configPath string, s Settings, opts MaterializeOptions) error {
	if !opts.Managed || !opts.RuntimeAuthPresent || opts.ToolHubEndpoint == "" ||
		s.Model == "" || s.ModelURL == "" || s.Timezone == "" {
		return fmt.Errorf("managed effective config identity is incomplete")
	}
	if err := toolhub.ValidateBackendEndpoint(opts.ToolHubEndpoint); err != nil {
		return fmt.Errorf("managed ToolHub endpoint: %w", err)
	}
	tokenEnv := opts.ToolHubTokenEnv
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	if !runtimeEnvName.MatchString(tokenEnv) {
		return fmt.Errorf("invalid managed ToolHub token environment")
	}
	if err := ValidateNativeToolsets(opts.NativeToolsets); err != nil {
		return fmt.Errorf("native_toolsets: %w", err)
	}
	if s.Tools == nil {
		s.Tools = map[string]ToolEntry{}
	}
	for name, entry := range nativeToolsEntries(opts.NativeToolsets) {
		s.Tools[name] = entry
	}
	expected := managedHermesConfig(s)
	expected["mcp_servers"] = M{"toolhub": M{
		"url": opts.ToolHubEndpoint, "timeout": toolHubCallTimeoutSeconds,
		"skip_preflight": true, "headers": M{"Authorization": "Bearer ${" + tokenEnv + "}"},
	}}
	if opts.ToolHubReconnect {
		expected["approvals"] = M{"mcp_reload_confirm": false}
	}
	wantBody, err := yaml.Marshal(expected)
	if err != nil {
		return err
	}
	var want, got M
	if err := yaml.Unmarshal(wantBody, &want); err != nil {
		return err
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- runtime-owned effective config path.
	if err != nil {
		return fmt.Errorf("read managed effective config: %w", err)
	}
	if err := yaml.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("parse managed effective config: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("managed effective config differs from reviewed profile")
	}
	return nil
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
