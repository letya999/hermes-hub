package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// platform_usage reports space model consumption (ADR-0094 M31.8 S4): the
// relay meter's reserved request/token counts joined against the limits the
// mounted models.yaml declares. Both sources are operator-mounted files —
// when either is absent the tool honestly answers unavailable instead of
// inventing numbers.
func (g *Gateway) addPlatformUsageTool(server *mcp.Server, auth identity.Envelope) {
	if g == nil || g.Store == nil || server == nil {
		return
	}
	server.AddTool(&mcp.Tool{
		Name:        "platform_usage",
		Description: "Show space model consumption: reserved request/token counts per model and quota window against the limits declared in models.yaml. Reports unavailable when the space mounts no usage meter.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"period": map[string]any{"type": "string", "enum": []string{"5h", "1d", "1w"}, "description": "Restrict the report to one quota window length."},
			},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Period string `json:"period"`
		}
		if request != nil && len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("%w: platform_usage arguments: %v", api.ErrInvalid, err)
			}
		}
		body, err := g.platformUsage(auth, arguments.Period)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(body)
		return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
}

type usageMeterRecord struct {
	Until int64 `json:"until"`
	Count int64 `json:"count"`
}

type usageRegistryFile struct {
	Models map[string]struct {
		Ladder []struct {
			Limits []struct {
				Of     string `yaml:"of"`
				Amount int64  `yaml:"amount"`
				Per    string `yaml:"per"`
				Scope  string `yaml:"scope,omitempty"`
			} `yaml:"limits,omitempty"`
		} `yaml:"ladder"`
	} `yaml:"models"`
}

func (g *Gateway) platformUsage(auth identity.Envelope, period string) (map[string]any, error) {
	usagePath := strings.TrimSpace(os.Getenv("HUB_MODEL_USAGE_FILE"))
	registryPath := strings.TrimSpace(os.Getenv("HUB_MODEL_REGISTRY_FILE"))
	if usagePath == "" || registryPath == "" {
		return map[string]any{
			"available": false,
			"status":    "unavailable",
			"reason":    "no model usage meter is mounted in this space",
		}, nil
	}
	meter := map[string]usageMeterRecord{}
	raw, err := os.ReadFile(usagePath) // #nosec G304 -- operator-mounted usage path
	if err != nil {
		return map[string]any{"available": false, "status": "unavailable", "reason": "usage meter unreadable"}, nil
	}
	_ = json.Unmarshal(raw, &meter)
	registryRaw, err := os.ReadFile(registryPath) // #nosec G304 -- operator-mounted registry path
	if err != nil {
		return map[string]any{"available": false, "status": "unavailable", "reason": "models.yaml registry unreadable"}, nil
	}
	var registry usageRegistryFile
	if err := yaml.Unmarshal(registryRaw, &registry); err != nil {
		return map[string]any{"available": false, "status": "unavailable", "reason": "models.yaml registry unparsable"}, nil
	}
	now := time.Now().Unix()
	type window struct {
		Of    string `json:"of"`
		Per   string `json:"per"`
		Scope string `json:"scope"`
		Used  int64  `json:"used"`
		Limit int64  `json:"limit"`
		Until int64  `json:"until,omitempty"`
	}
	names := make([]string, 0, len(registry.Models))
	for name := range registry.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]map[string]any, 0, len(names))
	for _, name := range names {
		windows := []window{}
		for _, hop := range registry.Models[name].Ladder {
			for _, limit := range hop.Limits {
				if period != "" && limit.Per != period {
					continue
				}
				scope := limit.Scope
				if scope == "" {
					scope = "shared"
				}
				if scope == "personal" {
					scope = auth.PrincipalID
				}
				w := window{Of: limit.Of, Per: limit.Per, Scope: scope, Limit: limit.Amount}
				prefix := fmt.Sprintf("%s|%s|%s|%s|", name, scope, limit.Of, limit.Per)
				for key, rec := range meter {
					if strings.HasPrefix(key, prefix) && rec.Until > now {
						w.Used = rec.Count
						w.Until = rec.Until
						break
					}
				}
				windows = append(windows, w)
			}
		}
		if len(windows) > 0 {
			models = append(models, map[string]any{"model": name, "windows": windows})
		}
	}
	return map[string]any{
		"available": true,
		"models":    models,
		"note":      "Counts are conservative reservations made at call admission, not provider-billed totals.",
	}, nil
}
