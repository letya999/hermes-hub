package runtime

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

var runtimeEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

const defaultToolHubEndpoint = "http://127.0.0.1:8090/mcp"

// ToolHub source review/build runs in an isolated builder and may outlive the
// ordinary MCP call timeout. The user gets Hermes' heartbeat while this call
// is in flight, so do not kill it after the default two minutes.
const toolHubCallTimeoutSeconds = 1800

func toolHubEndpoint() string {
	endpoint := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
	if endpoint == "" && os.Getenv("HUB_TOOLHUB_AUTOSTART") == "true" {
		return defaultToolHubEndpoint
	}
	return endpoint
}

func applyToolHubConfig(configPath string) error {
	endpoint := toolHubEndpoint()
	if endpoint == "" {
		return nil
	}
	if err := toolhub.ValidateBackendEndpoint(endpoint); err != nil {
		return fmt.Errorf("HUB_TOOLHUB_ENDPOINT: %w", err)
	}
	tokenEnv := os.Getenv("HUB_TOOLHUB_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	if !runtimeEnvName.MatchString(tokenEnv) {
		return fmt.Errorf("HUB_TOOLHUB_TOKEN_ENV: invalid environment name")
	}
	if os.Getenv(tokenEnv) == "" {
		return fmt.Errorf("HUB_TOOLHUB_TOKEN_ENV %s is empty", tokenEnv)
	}
	body, err := os.ReadFile(configPath)
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
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false") {
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
