package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func checkProviderAliasOwnership(ctx context.Context, project string, run func(context.Context, ...string) ([]byte, error)) error {
	ids, err := run(ctx, "ps", "--filter", "label=com.docker.compose.service=cliproxy", "--format", "{{.ID}}")
	if err != nil {
		return fmt.Errorf("check running model proxies: %w", err)
	}
	for _, id := range strings.Fields(string(ids)) {
		body, err := run(ctx, "inspect", id)
		if err != nil {
			return fmt.Errorf("inspect running model proxy: %w", err)
		}
		var containers []struct {
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
			NetworkSettings struct {
				Networks map[string]json.RawMessage `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
			return fmt.Errorf("invalid Docker model proxy inspection result")
		}
		other := containers[0].Config.Labels["com.docker.compose.project"]
		if other == project {
			continue
		}
		if _, shared := containers[0].NetworkSettings.Networks["hermes-hub-runtime"]; shared {
			return fmt.Errorf("shared model proxy alias is already active in Compose project %q; stop that proxy before starting %q", other, project)
		}
	}
	return nil
}
