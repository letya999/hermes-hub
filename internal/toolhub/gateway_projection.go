package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/systemguide"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// projectionFingerprint covers every persisted input that can change a
// projected tools/list. Calls still reauthorize against live state.
func (g *Gateway) projectionFingerprint(auth identity.Envelope) ([32]byte, bool, error) {
	var empty [32]byte
	s := g.Store
	s.mu.RLock()
	if s.path == "" {
		s.mu.RUnlock()
		return empty, false, nil // in-memory tests and pending edits refresh every time
	}
	changed, version, err := s.changedOnDiskLocked()
	digest, stamp, sqlite := s.diskDigest, s.diskStamp, s.db != nil
	s.mu.RUnlock()
	if err != nil || changed {
		return empty, false, err
	}
	h := sha256.New()
	h.Write(digest[:])
	if sqlite {
		fmt.Fprintf(h, ":db:%d", version)
	} else {
		fmt.Fprintf(h, ":file:%d:%d", stamp.mtime, stamp.size)
	}
	if g.Governance != nil {
		body, err := os.ReadFile(g.Governance.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, false, err
		}
		h.Write(body)
	}
	if g.PassportRoot != "" {
		space := filepath.Join(g.PassportRoot, auth.PrincipalID)
		if _, err := os.Stat(filepath.Join(space, "passports", passportview.ApplyJournalFile)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return empty, false, fmt.Errorf("%w: passport apply incomplete", api.ErrUnauthorized)
		}
		current, err := os.ReadFile(filepath.Join(space, "passports", "current"))
		if err != nil {
			return empty, false, err
		}
		rev := strings.TrimSpace(string(current))
		if rev == "pending" || !filepath.IsLocal(rev) {
			return empty, false, fmt.Errorf("%w: invalid passport pointer", api.ErrUnauthorized)
		}
		h.Write([]byte(rev))
		for _, name := range passportview.Names {
			body, err := os.ReadFile(filepath.Join(space, "passports", rev, name+".yaml")) // #nosec G703 -- rev is validated filepath.IsLocal; names come from the fixed passport view list.
			if err != nil {
				return empty, false, err
			}
			h.Write(body)
		}
	}
	copy(empty[:], h.Sum(nil))
	return empty, true, nil
}

func (g *Gateway) projectionFor(token string) (*gatewayProjection, error) {
	g.mu.Lock()
	if projection, ok := g.projections[token]; ok {
		g.mu.Unlock()
		return projection, nil
	}
	auth, ok := g.Tokens[token]
	g.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: token projection", api.ErrUnauthorized)
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
		runHandler: mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
			Stateless: true, MaxRequestBodyBytes: maxGatewayBodyBytes,
			PropagateRequestCancellation: true, DisableLocalhostProtection: g.DisableLocalhostProtection,
		}),
	}
	if fingerprint, cacheable, err := g.projectionFingerprint(auth); err == nil && cacheable {
		projection.fingerprint = fingerprint
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

func (g *Gateway) projectedServer(auth identity.Envelope) (*mcp.Server, map[string]api.ToolSpec, map[string]bool, error) {
	activation, err := g.requireActivated(auth)
	if err != nil {
		return nil, nil, nil, err
	}
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
	server.AddTool(&mcp.Tool{Name: "platform_guide", Description: "Read the Hermes Hub system guide: concepts, MCP/connector/CLI formats, ownership, access, inventory and recovery evidence. Static documentation; no account data or mutations.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: systemguide.Skill}}}, nil
	})
	g.addPlatformStatusTool(server, auth)
	g.addPlatformBootstrapTool(server, auth)
	g.addPlatformUsageTool(server, auth)
	g.addToolsListTool(server, auth)
	// R6.1: a profile carrying a compiled core bound projects direct schemas
	// only for those names. Extended tools stay reachable through tools_list
	// and invoke — admission is re-resolved per call, so the boundary never
	// becomes an authority edge.
	core, err := g.Store.CoreToolNames(auth)
	if err != nil {
		return nil, nil, nil, err
	}
	tools := make(map[string]api.ToolSpec, len(projected))
	for _, projectedTool := range projected {
		if core != nil && !core[projectedTool.Name] {
			continue
		}
		g.addProjectedTool(server, auth, projectedTool)
		tools[projectedTool.Name] = projectedTool.Tool
	}
	if current, err := g.requireActivated(auth); err != nil || current != activation {
		return nil, nil, nil, fmt.Errorf("%w: passport activation changed during projection", api.ErrUnauthorized)
	}
	return server, tools, controls, nil
}

func (g *Gateway) allowedControls(auth identity.Envelope) (map[string]bool, error) {
	if g.Control == nil {
		return nil, nil
	}
	return g.Store.AllowedControlOperations(auth)
}

func (g *Gateway) addProjectedTool(server *mcp.Server, auth identity.Envelope, projected api.ProjectedTool) {
	name, tool := projected.Name, projected.Tool
	schema := g.projectedToolSchema(tool)
	// Annotations derive from the declared effect (tool-design 6.4): a read
	// tool is readOnlyHint, a terminal deletion carries destructiveHint.
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: tool.Effect == api.ReadEffect}
	if strings.HasSuffix(name, "_remove") {
		destructive := true
		annotations.DestructiveHint = &destructive
	}
	server.AddTool(&mcp.Tool{Name: name, Description: tool.Description, InputSchema: schema, Annotations: annotations}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Per-request correlation rides in the HTTP headers; the streamable
		// session detaches the outer request context, so RequestExtra.Header
		// is the only carrier that reaches the tool call itself.
		if extra := request.GetExtra(); extra != nil && extra.Header != nil {
			if corr := api.HeaderCorrelation(extra.Header.Get(api.HeaderJobID), extra.Header.Get(api.HeaderRunID)); corr.JobID != "" || corr.HermesRunID != "" {
				ctx = api.WithCallCorrelation(ctx, corr)
			}
		}
		arguments := map[string]any{}
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("%w: tool arguments: %v", api.ErrInvalid, err)
			}
		}
		return g.mcpCall(ctx, auth, name, arguments)
	})
}

// projectedToolSchema renders the MCP input schema for a projected tool. The
// skill tool's name argument is narrowed to the names the mounted effective
// index admits (SPEC-0083 R5.1); the runtime executor re-validates
// authoritatively, so a stale index degrades to an admission hint.
func (g *Gateway) projectedToolSchema(tool api.ToolSpec) any {
	var schema any = api.CliInputSchema(tool)
	if len(tool.InputSchema) > 0 {
		schema = tool.InputSchema
	}
	if tool.Name != "skill" {
		return schema
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HUB_SKILLS_EFFECTIVE"), "index.json")) // #nosec G703 -- runtime-configured read-only effective view mount
	if err != nil {
		return schema
	}
	var index struct {
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	if json.Unmarshal(raw, &index) != nil {
		return schema
	}
	if object, ok := schema.(map[string]any); ok {
		if properties, ok := object["properties"].(map[string]any); ok {
			names := make([]any, 0, len(index.Skills))
			for _, entry := range index.Skills {
				names = append(names, entry.Name)
			}
			properties["name"] = map[string]any{"type": "string", "enum": names}
		}
	}
	return schema
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
func (g *Gateway) notifyPrepareDone(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding) {
	data, err := json.Marshal(map[string]any{
		"event":         "toolhub.prepare_done",
		"onboarding_id": onboarding.OnboardingID,
		"phase":         onboarding.NoticePhase(),
		"definition_id": onboarding.DefinitionID,
		"repository":    onboarding.SourceURL,
	})
	if err != nil {
		return
	}
	g.Control.deliverOutcomeNotice(ctx, auth, &onboarding)
	g.mu.Lock()
	list := make([]*gatewayProjection, 0, len(g.projections))
	for _, projection := range g.projections {
		list = append(list, projection)
	}
	g.mu.Unlock()
	for _, projection := range list {
		if !envelopeEqual(projection.auth, auth) {
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
	projection.refreshMu.Lock()
	defer projection.refreshMu.Unlock()
	if fingerprint, cacheable, err := g.projectionFingerprint(projection.auth); err != nil {
		return err
	} else if cacheable {
		projection.mu.Lock()
		unchanged := projection.fingerprint == fingerprint
		projection.mu.Unlock()
		if unchanged {
			return nil
		}
	}
	activation, err := g.requireActivated(projection.auth)
	if err != nil {
		return err
	}
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
	core, err := g.Store.CoreToolNames(projection.auth)
	if err != nil {
		return err
	}
	next := make(map[string]api.ToolSpec, len(projected))
	for _, tool := range projected {
		if core != nil && !core[tool.Name] {
			continue
		}
		next[tool.Name] = tool.Tool
	}
	if current, err := g.requireActivated(projection.auth); err != nil || current != activation {
		return fmt.Errorf("%w: passport activation changed during projection", api.ErrUnauthorized)
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
		if core != nil && !core[tool.Name] {
			continue
		}
		if old, ok := projection.tools[tool.Name]; !ok || !reflect.DeepEqual(old, tool.Tool) {
			g.addProjectedTool(projection.server, projection.auth, tool)
		}
	}
	projection.tools = next
	if fingerprint, cacheable, err := g.projectionFingerprint(projection.auth); err == nil && cacheable {
		projection.fingerprint = fingerprint
	} else {
		projection.fingerprint = [32]byte{}
	}
	return nil
}

// envelopeEqual compares two envelopes field-for-field; Envelope.Scopes made
// the struct non-comparable.
func envelopeEqual(a, b identity.Envelope) bool {
	return reflect.DeepEqual(a, b)
}
