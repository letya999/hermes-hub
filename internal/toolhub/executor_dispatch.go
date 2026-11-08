package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/systemguide"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// execRemote runs a remote-executor member behind the PEP (ADR-0094 D2/D6).
// Remote tools never reach the in-runtime exec channel: runtime:connectors
// delegates to the connector facade; executor families without a hub
// backend report unavailable honestly instead of falling through to a
// generic unknown-tool error.
func (g *Gateway) execRemote(ctx context.Context, auth identity.Envelope, effective api.EffectiveBinding, tool api.ToolSpec, arguments map[string]any) (api.BackendResult, error) {
	if tool.Executor == "runtime:connectors" {
		if g.Control == nil {
			return api.BackendResult{IsError: true, Text: tool.Name + " is unavailable: connector control is not configured"}, nil
		}
		result, err := g.Control.InvokeConnector(ctx, auth, tool.Name, arguments)
		if err != nil {
			return api.BackendResult{}, err
		}
		structured, _ := json.Marshal(result)
		var body map[string]any
		_ = json.Unmarshal(structured, &body)
		return api.BackendResult{Structured: body, Text: result.Summary, IsError: result.State == api.OpStateFailed}, nil
	}
	if tool.Executor == "runtime:toolhub" {
		return g.execPlatform(ctx, auth, effective, tool, arguments)
	}
	if tool.Executor == "runtime:models" {
		return g.execModel(ctx, auth, tool, arguments)
	}
	return api.BackendResult{IsError: true, Text: fmt.Sprintf("%s is unavailable: %s", tool.Name, remoteBackendHint(tool.Executor))}, nil
}

// remoteBackendHint names the missing backend an unconfigured executor
// family needs, so an unavailable call tells the caller why (#443/#444 —
// the contract ships; hub-side backends land in follow-ups).
func remoteBackendHint(executor string) string {
	if executor == "flow:deep_research" {
		return "the deep-research flow backend is not enabled"
	}
	return fmt.Sprintf("executor %s is not configured on this ToolHub", executor)
}

// execPlatform routes pinned platform members to the same handlers the
// direct MCP surface registers — a projected call is admission- and
// result-identical to the direct wire (ADR-0094 D6).
func (g *Gateway) execPlatform(ctx context.Context, auth identity.Envelope, effective api.EffectiveBinding, tool api.ToolSpec, arguments map[string]any) (api.BackendResult, error) {
	body := func(out map[string]any, err error) (api.BackendResult, error) {
		if err != nil {
			return api.BackendResult{IsError: true, Text: err.Error()}, nil
		}
		encoded, _ := json.Marshal(out)
		return api.BackendResult{Structured: out, Text: string(encoded)}, nil
	}
	switch tool.Name {
	case "platform_status":
		return body(g.platformStatus(auth))
	case "platform_guide":
		return api.BackendResult{Text: systemguide.Skill}, nil
	case "platform_bootstrap":
		return body(g.platformBootstrap(auth, api.ArgString(arguments, "action"), api.ArgString(arguments, "step")))
	case "platform_usage":
		return body(g.platformUsage(auth, api.ArgString(arguments, "period")))
	case "tool_search":
		return body(g.toolsList(auth, api.ArgString(arguments, "query"), api.ArgString(arguments, "name"), 0))
	case "memory_search":
		return body(g.memorySearch(ctx, auth, arguments))
	case "memory_read":
		return body(g.memoryRead(ctx, auth, arguments))
	case "memory_card":
		return body(g.memoryCard(ctx, auth, api.CallCorrelationFrom(ctx), effective, arguments))
	case "memory_open":
		return body(g.knowledgeOpen(ctx, auth))
	case "tool_call":
		name := api.ArgString(arguments, "tool")
		nested, _ := arguments["arguments"].(map[string]any)
		if strings.TrimSpace(name) == "" {
			return api.BackendResult{}, fmt.Errorf("%w: tool_call arguments", api.ErrInvalid)
		}
		// A nested call is its own admitted operation: a fresh ToolCallID
		// keeps its durable OperationID distinct from the enclosing
		// dispatch, while run correlation and the per-run call charge are
		// inherited. Reusing the parent's id collides in the ledger and
		// misreports the inner call as uncertain.
		corr := api.CallCorrelationFrom(ctx)
		corr.ToolCallID = api.NewToolCallID()
		out, err := g.mcpCall(api.WithCallCorrelation(ctx, corr), auth, name, nested)
		if err != nil {
			return api.BackendResult{IsError: true, Text: err.Error()}, nil
		}
		return api.BackendResult{IsError: out.IsError, Structured: out.StructuredContent, Content: out.Content}, nil
	}
	return api.BackendResult{IsError: true, Text: fmt.Sprintf("%s is unavailable: no platform handler", tool.Name)}, nil
}
