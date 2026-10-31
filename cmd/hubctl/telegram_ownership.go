package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
)

// telegramOwnershipCheckApplies reports whether this space actually renders a
// communication-hub gateway. Member spaces declare ingress:telegram but
// deliver through the org gateway — checking bot ownership for them always
// conflicts with the org's live gateway.
func telegramOwnershipCheckApplies(s stack.Settings) bool {
	return s.RendersInfra() && s.Has("telegram")
}

// Running gateways with one bot token compete for Telegram updates.
func checkTelegramOwnership(ctx context.Context, project, token string, run func(context.Context, ...string) ([]byte, error)) error {
	if token == "" {
		return nil
	}
	ids, err := run(ctx, "ps", "--filter", "label=com.docker.compose.service=communication-hub", "--format", "{{.ID}}")
	if err != nil {
		return fmt.Errorf("check running Telegram gateways: %w", err)
	}
	for _, id := range strings.Fields(string(ids)) {
		body, err := run(ctx, "inspect", id)
		if err != nil {
			return fmt.Errorf("inspect running Telegram gateway: %w", err)
		}
		var containers []struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
				Env    []string          `json:"Env"`
			} `json:"Config"`
		}
		if err := json.Unmarshal(body, &containers); err != nil || len(containers) != 1 {
			return fmt.Errorf("invalid Docker gateway inspection result")
		}
		other := containers[0].Config.Labels["com.docker.compose.project"]
		if other == project {
			continue
		}
		for _, env := range containers[0].Config.Env {
			if env == "TELEGRAM_BOT_TOKEN="+token {
				return fmt.Errorf("telegram bot is already active in Compose project %q; stop that gateway before starting %q", other, project)
			}
		}
	}
	return nil
}
