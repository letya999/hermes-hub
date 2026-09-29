package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
)

const defaultToolHubEndpoint = "http://127.0.0.1:8090/mcp"

func toolHubEndpoint() string {
	endpoint := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
	if endpoint == "" && os.Getenv("HUB_TOOLHUB_AUTOSTART") == "true" {
		return defaultToolHubEndpoint
	}
	return endpoint
}

// materializeOptionsFromEnv builds the effective-config inputs from the
// environment the container was launched with; the same values are rendered
// host-side for the read-only effective config mount.
func materializeOptionsFromEnv() stack.MaterializeOptions {
	tokenEnv := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_TOKEN_ENV"))
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	return stack.MaterializeOptions{
		ToolHubEndpoint:    toolHubEndpoint(),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: os.Getenv(tokenEnv) != "",
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false"),
		SelfServicesPath:   filepath.Join(state, selfServicesFile),
	}
}
