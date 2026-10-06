package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/selfsettings"
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
func materializeOptionsFromEnv() (stack.MaterializeOptions, error) {
	tokenEnv := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_TOKEN_ENV"))
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	var web stack.WebSettings
	if raw := strings.TrimSpace(os.Getenv("HUB_MANAGED_WEB")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &web); err != nil {
			return stack.MaterializeOptions{}, fmt.Errorf("invalid HUB_MANAGED_WEB: %w", err)
		}
	}
	var imageGen media.ImageGen
	if raw := strings.TrimSpace(os.Getenv("HUB_MANAGED_IMAGE_GEN")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &imageGen); err != nil {
			return stack.MaterializeOptions{}, fmt.Errorf("invalid HUB_MANAGED_IMAGE_GEN: %w", err)
		}
	}
	selfSettingsPath := ""
	if os.Getenv("HUB_CAPABILITY_MODE") == "managed" {
		selfSettingsPath = filepath.Join(state, selfsettings.FileName)
	}
	return stack.MaterializeOptions{
		Managed:            os.Getenv("HUB_CAPABILITY_MODE") == "managed",
		ToolHubEndpoint:    toolHubEndpoint(),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: os.Getenv(tokenEnv) != "",
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false"),
		SelfServicesPath:   filepath.Join(state, selfServicesFile),
		NativeToolsets:     envList("HUB_NATIVE_TOOLSETS"),
		Web:                web,
		ImageGen:           imageGen,
		SelfSettingsPath:   selfSettingsPath,
	}, nil
}

func envList(name string) []string {
	var out []string
	for _, value := range strings.Split(os.Getenv(name), ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
