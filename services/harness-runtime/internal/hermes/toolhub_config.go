package hermes

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/stack"
)

func toolHubEndpoint() string {
	return strings.TrimSpace(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
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
	var contextCfg *stack.ContextConfig
	if raw := strings.TrimSpace(os.Getenv("HUB_MANAGED_CONTEXT")); raw != "" {
		var decoded stack.ContextConfig
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return stack.MaterializeOptions{}, fmt.Errorf("invalid HUB_MANAGED_CONTEXT: %w", err)
		}
		contextCfg = &decoded
	}
	var browserGrant stack.BrowserGrant
	if raw := strings.TrimSpace(os.Getenv("HUB_MANAGED_BROWSER")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &browserGrant); err != nil {
			return stack.MaterializeOptions{}, fmt.Errorf("invalid HUB_MANAGED_BROWSER: %w", err)
		}
	}
	return stack.MaterializeOptions{
		ToolHubEndpoint:    toolHubEndpoint(),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: os.Getenv(tokenEnv) != "",
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false"),
		NativeToolsets:     envList("HUB_NATIVE_TOOLSETS"),
		Web:                web,
		ImageGen:           imageGen,
		Context:            contextCfg,
		Browser:            browserGrant,
		User:               os.Getenv("HUB_USER_ID"),
		MemoryVault:        os.Getenv("HUB_MANAGED_MEMORY_VAULT") == "1",
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
