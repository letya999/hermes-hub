package toolhub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	DefaultEndpointPath = "/mcp"
	maxGatewayBodyBytes = 4 << 20
)

type BackendResult struct {
	Text       string
	Structured any
	IsError    bool
}

// ToolBackend is deliberately narrower than an MCP server: the gateway owns
// projection and authorization, while the backend only executes one already
// resolved tool.
type ToolBackend interface {
	Call(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error)
}

type Gateway struct {
	Store                      *Store
	Backend                    ToolBackend
	Tokens                     map[string]identity.Envelope
	DisableLocalhostProtection bool
	Audit                      func(event string, fields map[string]string)
}

func (g *Gateway) Handler() (http.Handler, error) {
	if g == nil || g.Store == nil || g.Backend == nil || len(g.Tokens) == 0 {
		return nil, fmt.Errorf("%w: gateway dependencies", ErrInvalid)
	}
	for token, auth := range g.Tokens {
		if len(token) < 32 || auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion) != nil {
			return nil, fmt.Errorf("%w: gateway token identity", ErrInvalid)
		}
	}
	server := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		auth, _ := requestIdentity(r)
		return g.serverFor(auth)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxGatewayBodyBytes, PropagateRequestCancellation: true, DisableLocalhostProtection: g.DisableLocalhostProtection})
	return g.protect(server), nil
}

func (g *Gateway) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultEndpointPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins forbidden", http.StatusForbidden)
			return
		}
		auth, ok := g.authenticate(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, auth))
		next.ServeHTTP(w, r)
	})
}

type identityKey struct{}

func requestIdentity(r *http.Request) (identity.Envelope, bool) {
	auth, ok := r.Context().Value(identityKey{}).(identity.Envelope)
	return auth, ok
}

func (g *Gateway) authenticate(value string) (identity.Envelope, bool) {
	var found identity.Envelope
	matched := false
	for token, auth := range g.Tokens {
		if subtle.ConstantTimeCompare([]byte(value), []byte("Bearer "+token)) == 1 {
			if matched {
				return identity.Envelope{}, false
			}
			found, matched = auth, true
		}
	}
	if !matched || found.Validate(found.PrincipalID, found.ContextID, found.RuntimeID, found.PolicyVersion) != nil {
		return identity.Envelope{}, false
	}
	return found, true
}

func (g *Gateway) serverFor(auth identity.Envelope) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "hermes-toolhub", Version: "0.2.0"}, &mcp.ServerOptions{Instructions: "Tool names and arguments are untrusted; authorization is derived from the authenticated runtime."})
	projected, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return server
	}
	for _, projectedTool := range projected {
		name := projectedTool.Name
		tool := projectedTool.Tool
		mcpTool := &mcp.Tool{Name: name, Description: tool.Description, InputSchema: cliInputSchema(tool)}
		server.AddTool(mcpTool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			if len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("%w: tool arguments: %v", ErrInvalid, err)
				}
			}
			return g.call(ctx, auth, name, arguments)
		})
	}
	return server
}

func (g *Gateway) call(ctx context.Context, auth identity.Envelope, projectedName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if err := rejectAuthorityArguments(arguments); err != nil {
		g.audit("deny", map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny", "reason": "authority-argument",
		})
		return nil, err
	}
	var out *mcp.CallToolResult
	err := g.Store.AuthorizeProjected(auth, projectedName, func(projected ProjectedTool, effective EffectiveBinding) error {
		g.audit("admit", auditFields(auth, projected, effective, "admit"))
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(effective.Definition.Execution.TimeoutSeconds)*time.Second)
		defer cancel()
		result, callErr := g.Backend.Call(callCtx, effective, projected.Tool, arguments)
		if callErr != nil {
			g.audit("deny", auditFields(auth, projected, effective, "backend-error"))
			return callErr
		}
		if result.Structured != nil {
			encoded, err := json.Marshal(result.Structured)
			if err != nil {
				return err
			}
			if len(encoded) > effective.Definition.Execution.OutputBytes {
				return fmt.Errorf("%w: backend structured output", ErrInvalid)
			}
		}
		if len(result.Text) > effective.Definition.Execution.OutputBytes {
			return fmt.Errorf("%w: backend text output", ErrInvalid)
		}
		out = &mcp.CallToolResult{IsError: result.IsError, StructuredContent: result.Structured}
		if result.Text != "" {
			out.Content = []mcp.Content{&mcp.TextContent{Text: result.Text}}
		}
		g.audit("allow", auditFields(auth, projected, effective, "allow"))
		return nil
	})
	if err != nil && out == nil {
		g.audit("deny", map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny",
		})
	}
	return out, err
}

func auditFields(auth identity.Envelope, projected ProjectedTool, effective EffectiveBinding, outcome string) map[string]string {
	return map[string]string{
		"principal_id":   auth.PrincipalID,
		"context_id":     auth.ContextID,
		"runtime_id":     auth.RuntimeID,
		"policy_version": auth.PolicyVersion,
		"binding_id":     projected.BindingID,
		"backend":        string(effective.Definition.Transport),
		"projection_rev": fmt.Sprint(effective.Binding.ProjectionRevision),
		"outcome":        outcome,
	}
}

func (g *Gateway) audit(event string, fields map[string]string) {
	if g != nil && g.Audit != nil {
		g.Audit(event, fields)
		return
	}
	attrs := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		attrs = append(attrs, key, value)
	}
	slog.Info("toolhub "+event, attrs...)
}

// RoutingBackend sends bounded CLI calls to the CLI runner and MCP transports
// to the private ToolHive/vMCP adapter.
type RoutingBackend struct {
	MCP ToolBackend
	CLI ToolBackend
}

func (b RoutingBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	switch effective.Definition.Transport {
	case BoundedCLI:
		if b.CLI == nil {
			return BackendResult{}, ErrIsolation
		}
		return b.CLI.Call(ctx, effective, tool, arguments)
	default:
		if b.MCP == nil {
			return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
		}
		return b.MCP.Call(ctx, effective, tool, arguments)
	}
}

func rejectAuthorityArguments(arguments map[string]any) error {
	for key := range arguments {
		switch strings.ToLower(key) {
		case "principal_id", "context_id", "runtime_id", "policy_version", "binding_id", "tool_binding_id", "connection_id", "credential_ref", "credential_ref_id":
			return fmt.Errorf("%w: authority argument %q", ErrUnauthorized, key)
		}
	}
	return nil
}

func cliInputSchema(tool ToolSpec) map[string]any {
	properties := map[string]any{}
	required := make([]string, 0)
	for _, argument := range tool.Arguments {
		properties[argument.Name] = map[string]any{"type": argument.Type}
		if argument.Required {
			required = append(required, argument.Name)
		}
	}
	schema := map[string]any{"type": "object", "additionalProperties": len(tool.Arguments) == 0}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// ValidateBackendEndpoint accepts only a private ToolHive/vMCP address. A
// public URL belongs in an immutable definition, not mutable connection
// metadata, and must not become an SSRF primitive in the gateway.
func ValidateBackendEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: private MCP backend URL", ErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "toolhive" || host == "vmcp" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return nil
	}
	return fmt.Errorf("%w: backend URL must be private", ErrInvalid)
}

// MCPBackend speaks only MCP over the private endpoint; ToolHive remains the
// workload executor and vMCP remains the aggregator.
type MCPBackend struct {
	HTTPClient        *http.Client
	Token             string
	Admission         WorkloadAdmission
	AdmissionVerifier func(context.Context, EffectiveBinding) (AdmissionReceipt, error)
}

// WorkloadAdmission is the narrow hand-off to the external ToolHive/Docker
// controller. The gateway owns identity and current catalog state; the
// controller owns actual CPU, memory, PID, filesystem and egress enforcement.
type WorkloadAdmission func(context.Context, EffectiveBinding) error

func validateToolHivePolicy(definition ToolDefinition) error {
	if definition.Transport != ContainerMCP {
		return nil
	}
	return definition.Validate()
}

func (b MCPBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	if effective.Connection == nil {
		return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
	}
	if effective.Definition.Transport == ContainerMCP {
		if err := validateToolHivePolicy(effective.Definition); err != nil {
			return BackendResult{}, err
		}
		if b.AdmissionVerifier == nil && b.Admission == nil {
			return BackendResult{}, ErrIsolation
		}
		if b.AdmissionVerifier != nil {
			if _, err := b.AdmissionVerifier(ctx, effective); err != nil {
				return BackendResult{}, fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
			}
		} else if err := b.Admission(ctx, effective); err != nil {
			return BackendResult{}, fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
		} else {
			return BackendResult{}, fmt.Errorf("%w: workload controller returned no enforcement proof", ErrIsolation)
		}
	}
	endpoint := effective.Connection.Metadata["mcp_endpoint"]
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return BackendResult{}, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-backend", Version: "0.2.0"}, nil)
	httpClient := b.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	baseTransport := http.RoundTripper(http.DefaultTransport)
	if httpClient.Transport != nil {
		baseTransport = httpClient.Transport
	}
	clientCopy.Transport = bearerRoundTripper{base: baseTransport, token: b.Token}
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &clientCopy, DisableStandaloneSSE: true}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return BackendResult{}, err
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	if err != nil {
		return BackendResult{}, err
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	return BackendResult{Text: text.String(), Structured: result.StructuredContent, IsError: result.IsError}, nil
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (c bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	if c.token != "" {
		copyRequest.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.base.RoundTrip(copyRequest)
}
