package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// platform_bootstrap is the pinned first-run guide (ADR-0094 M31.8 S3). It
// returns a bounded list of setup steps, each "done" when recorded complete or
// already evident from store state, and answers done once every step is
// closed. Recorded marks live in one small JSON file under HUB_STATE so a
// restart never re-asks a finished step.
var bootstrapSteps = []struct {
	name string
	hint string
}{
	{"profile", "a tool profile selects at least one toolset"},
	{"connector", "at least one connector is installed"},
	{"workspace", "the owner confirmed the workspace is ready"},
}

func (g *Gateway) addPlatformBootstrapTool(server *mcp.Server, auth identity.Envelope) {
	if g == nil || g.Store == nil || server == nil {
		return
	}
	server.AddTool(&mcp.Tool{
		Name:        "platform_bootstrap",
		Description: "Show pending first-run setup steps for this space, or mark one finished with action=done and step=<name>. Idempotent: completed steps stay done across restarts.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"done"}, "description": "Pass done together with step to record a finished step."},
				"step":   map[string]any{"type": "string", "description": "Step name from the pending list."},
			},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Action string `json:"action"`
			Step   string `json:"step"`
		}
		if request != nil && len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("%w: platform_bootstrap arguments: %v", api.ErrInvalid, err)
			}
		}
		body, err := g.platformBootstrap(auth, arguments.Action, arguments.Step)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(body)
		return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
}

func (g *Gateway) platformBootstrap(auth identity.Envelope, action, step string) (map[string]any, error) {
	recorded, err := g.bootstrapRecorded(auth)
	if err != nil {
		return nil, err
	}
	if action != "" {
		if action != "done" || !bootstrapStepKnown(step) {
			return nil, fmt.Errorf("%w: platform_bootstrap needs action=done with a known step", api.ErrInvalid)
		}
		if !recorded[step] {
			recorded[step] = true
			if err := g.bootstrapSave(auth, recorded); err != nil {
				return nil, err
			}
		}
	}
	evident, err := g.bootstrapEvident(auth)
	if err != nil {
		return nil, err
	}
	steps := make([]map[string]any, 0, len(bootstrapSteps))
	done := true
	for _, decl := range bootstrapSteps {
		closed := recorded[decl.name] || evident[decl.name]
		state := "pending"
		if closed {
			state = "done"
		} else {
			done = false
		}
		steps = append(steps, map[string]any{"name": decl.name, "state": state, "hint": decl.hint})
	}
	return map[string]any{"done": done, "steps": steps}, nil
}

func bootstrapStepKnown(name string) bool {
	for _, decl := range bootstrapSteps {
		if decl.name == name {
			return true
		}
	}
	return false
}

// bootstrapEvident reports steps the store state already proves: a profile
// with selections admits tools, an installed connection proves connectors.
func (g *Gateway) bootstrapEvident(auth identity.Envelope) (map[string]bool, error) {
	evident := map[string]bool{}
	projected, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return nil, err
	}
	evident["profile"] = len(projected) > 0
	evident["connector"] = len(g.Store.Connections()) > 0
	return evident, nil
}

func (g *Gateway) bootstrapPath(auth identity.Envelope) (string, error) {
	base := strings.TrimSpace(os.Getenv("HUB_STATE"))
	if base == "" {
		return "", fmt.Errorf("%w: HUB_STATE is not configured", api.ErrInvalid)
	}
	name := strings.ReplaceAll(auth.PrincipalID, string(filepath.Separator), "_") + ".bootstrap.json"
	return filepath.Join(base, "toolhub", name), nil
}

func (g *Gateway) bootstrapRecorded(auth identity.Envelope) (map[string]bool, error) {
	path, err := g.bootstrapPath(auth)
	if err != nil {
		return nil, err
	}
	recorded := map[string]bool{}
	raw, err := os.ReadFile(path) // #nosec G304 -- path derives from operator-mounted HUB_STATE
	if err != nil {
		return recorded, nil
	}
	var steps map[string]bool
	if json.Unmarshal(raw, &steps) == nil {
		for step, ok := range steps {
			if ok && bootstrapStepKnown(step) {
				recorded[step] = true
			}
		}
	}
	return recorded, nil
}

func (g *Gateway) bootstrapSave(auth identity.Envelope, recorded map[string]bool) error {
	path, err := g.bootstrapPath(auth)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(recorded)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
