package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool_search is the live caller-scoped inventory (ADR-0089 D1-D4). It is
// registered unconditionally beside platform_guide because it reports only the
// caller's own admitted surface — the same inputs that feed tools/list — so
// the answer stays true when a connector or MCP tool is enabled in this or a
// neighbouring session, without reconnect, respawn or prompt regeneration.
func (g *Gateway) addToolsListTool(server *mcp.Server, auth identity.Envelope) {
	if g == nil || g.Store == nil || server == nil {
		return
	}
	server.AddTool(&mcp.Tool{
		Name:        "tool_search",
		Description: "List the ToolHub tools currently admitted for you: exact names, re-read on every call, including tools enabled during this or another session. The toolsets block is the authoritative answer to which tools.yaml toolsets are selected — pass toolset to list one set's members, name for one tool's full spec, or query to filter. A toolset row with state:pending is authored but not connected yet — read connector_status for its onboarding stage before telling the user anything is missing. A name listed here but missing from your visible tool list is extended inventory, callable through tool_call — the visible list is a bounded core projection, never a permission boundary. Call this before telling the user a capability is missing.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "maxLength": 256, "description": "Case-insensitive substring over tool names, descriptions, parameter names and definition ids. Matching entries include description and input schema."},
				"name":    map[string]any{"type": "string", "maxLength": 128, "description": "Exact projected tool name; returns its full spec."},
				"toolset": map[string]any{"type": "string", "maxLength": 128, "description": "Authored toolset name from the toolsets block; restricts results to that set's member tools."},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "description": "Maximum tools returned."},
			},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Query   string `json:"query"`
			Name    string `json:"name"`
			Toolset string `json:"toolset"`
			Limit   int    `json:"limit"`
		}
		if request != nil && len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("%w: tools_list arguments: %v", api.ErrInvalid, err)
			}
		}
		body, err := g.toolsList(auth, arguments.Query, arguments.Name, arguments.Toolset, arguments.Limit)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(body)
		return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
	})
}

// toolQueryMatch implements the r5 search contract (ADR-0094 M31.8 S5): the
// query matches the projected name, description text, input parameter names
// and the backing definition_id — case-insensitive substrings.
func toolQueryMatch(tool api.ProjectedTool, query string) bool {
	if strings.Contains(strings.ToLower(tool.Name), query) ||
		strings.Contains(strings.ToLower(tool.Tool.Description), query) ||
		strings.Contains(strings.ToLower(tool.DefinitionID), query) {
		return true
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(tool.Tool.InputSchema, &schema) == nil {
		for param := range schema.Properties {
			if strings.Contains(strings.ToLower(param), query) {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) toolsList(auth identity.Envelope, query, name, toolset string, limit int) (map[string]any, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	name = strings.TrimPrefix(strings.TrimSpace(name), toolhubNamePrefix)
	toolset = strings.TrimSpace(toolset)
	sets, toolsetByName, err := g.Store.ProfileToolsets(auth)
	if err != nil {
		return nil, err
	}
	var projected []api.ProjectedTool
	fastExact := false
	if name != "" {
		if tool, effective, err := g.Store.ResolveProjectedTool(auth, name); err == nil {
			fastExact = true
			allowed := true
			if g.Governance != nil {
				decision, err := g.governanceDecision(auth, tool.Tool, effective)
				if err != nil {
					return nil, err
				}
				allowed = decision.AllowsToolEffect(string(tool.Tool.Effect))
			}
			if allowed && (query == "" || toolQueryMatch(tool, query)) {
				projected = []api.ProjectedTool{tool}
			}
		}
	}
	if !fastExact {
		var err error
		projected, err = g.Store.ListProjectedTools(auth)
		if err != nil {
			return nil, err
		}
		if projected, err = g.filterProjectedTools(auth, projected); err != nil {
			return nil, err
		}
	}
	detailed := query != "" || name != ""
	entries := make([]map[string]any, 0, len(projected))
	matched := 0
	admitted := make(map[string]bool, len(projected))
	sourceCounts := map[string]int{}
	for _, tool := range projected {
		admitted[tool.Name] = true
		sourceCounts[tool.DefinitionID]++
		if toolset != "" && toolsetByName[tool.Name] != toolset {
			continue
		}
		if name != "" && tool.Name != name {
			continue
		}
		if query != "" && !toolQueryMatch(tool, query) {
			continue
		}
		matched++
		if limit > 0 && len(entries) >= limit {
			continue
		}
		entry := map[string]any{
			"name":       tool.Name,
			"definition": tool.DefinitionID,
			"effect":     string(tool.Tool.Effect),
		}
		if set := toolsetByName[tool.Name]; set != "" {
			entry["toolset"] = set
		}
		if detailed {
			entry["description"] = tool.Tool.Description
			entry["input_schema"] = g.projectedToolSchema(tool.Tool)
		}
		entries = append(entries, entry)
	}
	controls, err := g.allowedControls(auth)
	if err != nil {
		return nil, err
	}
	controlNames := make([]string, 0, len(controls))
	for operation := range controls {
		controlNames = append(controlNames, operation)
	}
	sort.Strings(controlNames)
	body := map[string]any{
		"tools":    entries,
		"controls": controlNames,
		"total":    matched,
		"returned": len(entries),
		"note":     "Admitted ToolHub tools, re-read on every call — including tools enabled during this or another session. A name listed here but missing from your visible tool list is callable through tool_call({tool, arguments}). Hub workspace tools (mcp__hub__*) and the skills inventory (skill_search) live on the hub server.",
	}
	// Inventory blocks answer "what is selected" from the same caller-scoped
	// projection that authorizes calls — counts are live admitted tools, never
	// name-prefix inference. Exact-name describes stay light: the blocks are
	// list/search output, not part of one tool's spec.
	if name == "" {
		if len(sets) > 0 {
			totals, live := map[string]int{}, map[string]int{}
			for toolName, setName := range toolsetByName {
				totals[setName]++
				if admitted[toolName] {
					live[setName]++
				}
			}
			core, extended := make([]map[string]any, 0), make([]map[string]any, 0)
			for _, set := range sets {
				row := map[string]any{"name": set.Name, "via": set.Via, "tools_admitted": live[set.Name], "tools_total": totals[set.Name]}
				// Pending: an authored connector toolset whose selections are
				// not materialized yet — no active binding, or the connector is
				// still onboarding. The set is real (tools.yaml), the tools are
				// not callable yet; connector_status carries the lifecycle.
				if strings.HasPrefix(set.Via, "mcp") && totals[set.Name] == 0 {
					row["state"] = "pending"
				}
				if set.Placement == "core" {
					core = append(core, row)
				} else {
					extended = append(extended, row)
				}
			}
			body["toolsets"] = map[string]any{"core": core, "extended": extended}
		}
		if len(sourceCounts) > 0 {
			sourceIDs := make([]string, 0, len(sourceCounts))
			for id := range sourceCounts {
				sourceIDs = append(sourceIDs, id)
			}
			sort.Strings(sourceIDs)
			sources := make([]map[string]any, 0, len(sourceIDs))
			for _, id := range sourceIDs {
				sources = append(sources, map[string]any{"definition_id": id, "tool_count": sourceCounts[id]})
			}
			body["sources"] = sources
		}
	}
	// Denied selections surface as unavailable: the agent learns the missing
	// capability.operation or the capability_rules reason instead of guessing
	// the tool vanished.
	if name == "" || matched == 0 {
		unavailable := make([]map[string]any, 0)
		if denials, err := g.Store.RightsDenials(auth); err == nil && len(denials) > 0 {
			for _, denial := range denials {
				if name != "" && denial.Name != name {
					continue
				}
				unavailable = append(unavailable, map[string]any{"name": denial.Name, "unavailable": true, "missing_right": denial.Missing})
			}
			if name != "" && matched == 0 {
				for _, denial := range denials {
					if denial.Name == name {
						body["missing_right"] = denial.Missing
						break
					}
				}
			}
		}
		if denials, err := g.Store.CapabilityDenials(auth); err == nil && len(denials) > 0 {
			for _, denial := range denials {
				if name != "" && denial.Name != name {
					continue
				}
				unavailable = append(unavailable, map[string]any{"name": denial.Name, "unavailable": true, "reason": denial.Reason})
			}
			if name != "" && matched == 0 {
				for _, denial := range denials {
					if denial.Name == name {
						body["reason"] = denial.Reason
						break
					}
				}
			}
		}
		if len(unavailable) > 0 {
			body["unavailable"] = unavailable
		}
	}
	if name != "" {
		body["found"] = matched > 0
	}
	return body, nil
}
