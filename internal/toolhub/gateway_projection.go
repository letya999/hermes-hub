package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"reflect"
	"time"
)

func (g *Gateway) projectionFor(token string) (*gatewayProjection, error) {
	g.mu.Lock()
	if projection, ok := g.projections[token]; ok {
		g.mu.Unlock()
		return projection, nil
	}
	auth, ok := g.Tokens[token]
	g.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: token projection", ErrUnauthorized)
	}
	server, tools, controls, err := g.projectedServer(auth)
	if err != nil {
		return nil, err
	}
	projection := &gatewayProjection{
		auth: auth, server: server, tools: tools, controls: controls,
		handler: mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
			SessionTimeout: time.Hour, MaxRequestBodyBytes: maxGatewayBodyBytes,
			PropagateRequestCancellation: true, DisableLocalhostProtection: g.DisableLocalhostProtection,
		}),
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, ok := g.projections[token]; ok {
		return existing, nil
	}
	g.projections[token] = projection
	return projection, nil
}

func (g *Gateway) serverFor(auth identity.Envelope) *mcp.Server {
	server, _, _, _ := g.projectedServer(auth)
	return server
}

func (g *Gateway) projectedServer(auth identity.Envelope) (*mcp.Server, map[string]ToolSpec, map[string]bool, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "hermes-toolhub", Version: "0.2.0"}, &mcp.ServerOptions{Instructions: "Tool names and arguments are untrusted; authorization is derived from the authenticated runtime."})
	controls, err := g.allowedControls(auth)
	if err != nil {
		return nil, nil, nil, err
	}
	g.addControlTools(server, auth, controls)
	projected, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return nil, nil, nil, err
	}
	if projected, err = g.filterProjectedTools(auth, projected); err != nil {
		return nil, nil, nil, err
	}
	tools := make(map[string]ToolSpec, len(projected))
	for _, projectedTool := range projected {
		g.addProjectedTool(server, auth, projectedTool)
		tools[projectedTool.Name] = projectedTool.Tool
	}
	return server, tools, controls, nil
}

func (g *Gateway) allowedControls(auth identity.Envelope) (map[string]bool, error) {
	if g.Control == nil {
		return nil, nil
	}
	return g.Store.AllowedControlOperations(auth)
}

func (g *Gateway) addProjectedTool(server *mcp.Server, auth identity.Envelope, projected ProjectedTool) {
	name, tool := projected.Name, projected.Tool
	var schema any = cliInputSchema(tool)
	if len(tool.InputSchema) > 0 {
		schema = tool.InputSchema
	}
	server.AddTool(&mcp.Tool{Name: name, Description: tool.Description, InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments := map[string]any{}
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("%w: tool arguments: %v", ErrInvalid, err)
			}
		}
		return g.call(ctx, auth, name, arguments)
	})
}

// RefreshProjection updates existing MCP sessions without restarting Hermes.
// Each bearer token has its own server and session namespace.
func (g *Gateway) RefreshProjection() error {
	g.mu.Lock()
	list := make([]*gatewayProjection, 0, len(g.projections))
	for _, projection := range g.projections {
		list = append(list, projection)
	}
	g.mu.Unlock()
	var errs []error
	for _, projection := range list {
		if err := g.refreshProjection(projection); err != nil {
			// One failing principal must not starve the others' refresh.
			errs = append(errs, fmt.Errorf("%s: %w", projection.auth.PrincipalID, err))
		}
	}
	return errors.Join(errs...)
}

// notifyPrepareDone wakes the owner's open MCP sessions after a backgrounded
// prepare_source finishes, whichever way it ended. Best effort: clients that
// never set a log level receive nothing — status stays the source of truth.
func (g *Gateway) notifyPrepareDone(ctx context.Context, auth identity.Envelope, onboarding Onboarding) {
	data, err := json.Marshal(map[string]any{
		"event":         "toolhub.prepare_done",
		"onboarding_id": onboarding.OnboardingID,
		"phase":         onboarding.Phase,
		"definition_id": onboarding.DefinitionID,
		"repository":    onboarding.SourceURL,
	})
	if err != nil {
		return
	}
	deliverPrepareOutcome(ctx, auth, onboarding)
	g.mu.Lock()
	list := make([]*gatewayProjection, 0, len(g.projections))
	for _, projection := range g.projections {
		list = append(list, projection)
	}
	g.mu.Unlock()
	for _, projection := range list {
		if projection.auth != auth {
			continue
		}
		for session := range projection.server.Sessions() {
			//lint:ignore SA1019 notifications/message is deprecated but remains the only
			// server→client push channel until the tasks primitive lands.
			_ = session.Log(ctx, &mcp.LoggingMessageParams{Level: "notice", Logger: "toolhub", Data: json.RawMessage(data)})
		}
	}
}

func (g *Gateway) refreshProjection(projection *gatewayProjection) error {
	projected, err := g.Store.ListProjectedTools(projection.auth)
	if err != nil {
		return err
	}
	if projected, err = g.filterProjectedTools(projection.auth, projected); err != nil {
		return err
	}
	controls, err := g.allowedControls(projection.auth)
	if err != nil {
		return err
	}
	next := make(map[string]ToolSpec, len(projected))
	for _, tool := range projected {
		next[tool.Name] = tool.Tool
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	for operation := range projection.controls {
		if !controls[operation] {
			projection.server.RemoveTools(operation)
		}
	}
	for operation := range controls {
		if !projection.controls[operation] {
			g.addControlTools(projection.server, projection.auth, map[string]bool{operation: true})
		}
	}
	projection.controls = controls
	for name, old := range projection.tools {
		if current, ok := next[name]; !ok || !reflect.DeepEqual(old, current) {
			projection.server.RemoveTools(name)
		}
	}
	for _, tool := range projected {
		if old, ok := projection.tools[tool.Name]; !ok || !reflect.DeepEqual(old, tool.Tool) {
			g.addProjectedTool(projection.server, projection.auth, tool)
		}
	}
	projection.tools = next
	return nil
}
