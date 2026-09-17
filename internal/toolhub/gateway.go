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
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	DefaultEndpointPath = "/mcp"
	maxGatewayBodyBytes = 4 << 20
	legacyMCPVersion    = "2025-11-25"
)

type BackendResult struct {
	Text       string
	Structured any
	IsError    bool
	// Receipt is the provider's own mutation proof (for example a Google event
	// identity or a Slack channel plus message timestamp). It is metadata only
	// and never carries a credential value.
	Receipt string
}

// ToolBackend is deliberately narrower than an MCP server: the gateway owns
// projection and authorization, while the backend only executes one already
// resolved tool.
type ToolBackend interface {
	Call(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error)
}

type envBackend interface {
	CallEnv(context.Context, EffectiveBinding, ToolSpec, map[string]any, map[string]string) (BackendResult, error)
}

type Gateway struct {
	Store                      *Store
	Backend                    ToolBackend
	Tokens                     map[string]identity.Envelope
	DisableLocalhostProtection bool
	Audit                      func(event string, fields map[string]string)
	AuditWrite                 func(event string, fields map[string]string) error
	Injector                   func(context.Context, EffectiveBinding) (map[string]string, func() error, error)
	Control                    *ControlPlane
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
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		auth, _ := requestIdentity(r)
		return g.serverFor(auth)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxGatewayBodyBytes, PropagateRequestCancellation: true, DisableLocalhostProtection: g.DisableLocalhostProtection})
	mux := http.NewServeMux()
	mux.Handle(DefaultEndpointPath, g.protect(mcpHandler))
	mux.Handle("/credentials/", http.HandlerFunc(g.serveCredentials))
	mux.Handle("/oauth/callback", http.HandlerFunc(g.serveOAuthCallback))
	return mux, nil
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
		corr := headerCorrelation(r.Header.Get(HeaderJobID), r.Header.Get(HeaderRunID))
		ctx := context.WithValue(r.Context(), identityKey{}, auth)
		r = r.WithContext(withCallCorrelation(ctx, corr))
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
	g.addControlTools(server, auth)
	projected, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return server
	}
	for _, projectedTool := range projected {
		name := projectedTool.Name
		tool := projectedTool.Tool
		var schema any = cliInputSchema(tool)
		if len(tool.InputSchema) > 0 {
			schema = tool.InputSchema
		}
		mcpTool := &mcp.Tool{Name: name, Description: tool.Description, InputSchema: schema}
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
	corr := callCorrelationFrom(ctx)
	if corr.ToolCallID == "" {
		corr.ToolCallID = newToolCallID()
	}
	ctx = withCallCorrelation(ctx, corr)
	if err := RejectAuthorityArguments(arguments); err != nil {
		_ = g.audit("deny", mergeAudit(map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny", "reason": "authority-argument",
		}, corr))
		return nil, err
	}
	var out *mcp.CallToolResult
	err := g.Store.AuthorizeProjected(auth, projectedName, func(projected ProjectedTool, effective EffectiveBinding) error {
		if len(projected.Tool.InputSchema) > 0 {
			var schema jsonschema.Schema
			if json.Unmarshal(projected.Tool.InputSchema, &schema) != nil {
				return ErrInvalid
			}
			resolved, err := schema.Resolve(nil)
			if err != nil || resolved.Validate(arguments) != nil {
				return fmt.Errorf("%w: MCP arguments do not match admitted schema", ErrInvalid)
			}
		}
		if err := g.audit("admit", mergeAudit(auditFields(auth, projected, effective, "admit"), corr)); err != nil {
			return err
		}
		env := map[string]string{}
		var wipe func() error
		if g.Injector != nil {
			var injErr error
			env, wipe, injErr = g.Injector(ctx, effective)
			if injErr != nil {
				return injErr
			}
			if wipe != nil {
				defer wipe()
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(effective.Definition.Execution.TimeoutSeconds)*time.Second)
		defer cancel()
		var result BackendResult
		var callErr error
		if caller, ok := g.Backend.(envBackend); ok {
			result, callErr = caller.CallEnv(callCtx, effective, projected.Tool, arguments, env)
		} else {
			result, callErr = g.Backend.Call(callCtx, effective, projected.Tool, arguments)
		}
		if callErr != nil {
			_ = g.audit("deny", mergeAudit(auditFields(auth, projected, effective, "backend-error"), corr))
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
		fields := auditFields(auth, projected, effective, "allow")
		if receipt := SafeReceipt(result.Receipt); receipt != "" {
			fields["receipt"] = receipt
		}
		return g.audit("allow", mergeAudit(fields, corr))
	})
	if err != nil && out == nil {
		_ = g.audit("deny", mergeAudit(map[string]string{
			"principal_id": auth.PrincipalID, "context_id": auth.ContextID, "runtime_id": auth.RuntimeID,
			"policy_version": auth.PolicyVersion, "outcome": "deny",
		}, corr))
	}
	return out, err
}

// CallAuthorized is the protected host CLI path; it uses the identical
// authorization, injection, output and audit fences as the MCP handler.
func (g *Gateway) CallAuthorized(ctx context.Context, auth identity.Envelope, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	return g.call(ctx, auth, name, arguments)
}

func auditFields(auth identity.Envelope, projected ProjectedTool, effective EffectiveBinding, outcome string) map[string]string {
	fields := map[string]string{
		"principal_id":   auth.PrincipalID,
		"context_id":     auth.ContextID,
		"runtime_id":     auth.RuntimeID,
		"policy_version": auth.PolicyVersion,
		"binding_id":     projected.BindingID,
		"backend":        string(effective.Definition.Transport),
		"projection_rev": fmt.Sprint(effective.Binding.ProjectionRevision),
		"outcome":        outcome,
	}
	if effective.Connection != nil {
		fields["connection_id"] = effective.Connection.ConnectionID
		fields["connection_revision"] = fmt.Sprint(effective.Connection.Revision)
	}
	if effective.Credential != nil {
		fields["credential_revision"] = fmt.Sprint(effective.Credential.Revision)
	}
	return fields
}

// SafeReceipt bounds a provider mutation receipt before it reaches the audit
// ledger. An oversized or control-character receipt is dropped rather than
// truncated, so a provider cannot smuggle content into control-plane records.
func SafeReceipt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return value
}

func mergeAudit(fields map[string]string, corr callCorrelation) map[string]string {
	if fields == nil {
		fields = map[string]string{}
	}
	if corr.JobID != "" {
		fields["job_id"] = corr.JobID
	}
	if corr.HermesRunID != "" {
		fields["hermes_run_id"] = corr.HermesRunID
	}
	if corr.ToolCallID != "" {
		fields["tool_call_id"] = corr.ToolCallID
	}
	return fields
}

func (g *Gateway) audit(event string, fields map[string]string) error {
	if g != nil && g.AuditWrite != nil {
		return g.AuditWrite(event, fields)
	}
	if g != nil && g.Audit != nil {
		g.Audit(event, fields)
		return nil
	}
	attrs := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		attrs = append(attrs, key, value)
	}
	slog.Info("toolhub "+event, attrs...)
	return nil
}

// RoutingBackend sends bounded CLI calls to the CLI runner, provider API calls
// to the in-process official REST data plane and MCP transports to the private
// ToolHive/vMCP adapter.
type RoutingBackend struct {
	MCP      ToolBackend
	CLI      ToolBackend
	Provider ToolBackend
}

func (b RoutingBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b RoutingBackend) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	var backend ToolBackend
	switch effective.Definition.Transport {
	case BoundedCLI:
		backend = b.CLI
	case ProviderAPI:
		backend = b.Provider
	default:
		backend = b.MCP
	}
	if backend == nil {
		if effective.Definition.Transport == BoundedCLI || effective.Definition.Transport == ProviderAPI {
			return BackendResult{}, ErrIsolation
		}
		return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
	}
	if caller, ok := backend.(envBackend); ok {
		return caller.CallEnv(ctx, effective, tool, arguments, environment)
	}
	return backend.Call(ctx, effective, tool, arguments)
}

// RejectAuthorityArguments denies model-supplied identity, binding or
// credential selectors. Authorization comes only from the authenticated
// runtime and the resolved binding.
func RejectAuthorityArguments(arguments map[string]any) error {
	for key := range arguments {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "principal_id", "context_id", "runtime_id", "policy_version", "binding_id", "tool_binding_id", "connection_id", "credential_ref", "credential_ref_id", "owner", "owner_id", "user", "user_id", "account_id", "locator", "credential_locator", "backend", "backend_url", "mcp_endpoint", "policy", "grant", "grant_id", "issued_by", "store_owner":
			return fmt.Errorf("%w: authority argument %q", ErrUnauthorized, key)
		}
		if credentialPattern.MatchString(key) {
			return fmt.Errorf("%w: credential value argument %q", ErrUnauthorized, key)
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
	Root              string
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
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b MCPBackend) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	if strings.HasPrefix(effective.Definition.DefinitionID, "google-workspace-") {
		return (GoogleWorkspaceBackend{HTTP: b.HTTPClient}).CallEnv(ctx, effective, tool, arguments, environment)
	}
	if effective.Connection == nil && effective.Definition.Transport != ContainerMCP {
		return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
	}
	telegram := strings.HasPrefix(effective.Definition.DefinitionID, "telegram-account-")
	if telegram && (effective.Definition.Transport != ContainerMCP || environment["TELEGRAM_ACCOUNT_ID"] == "" || environment["TELEGRAM_ACCOUNT_ID"] != effective.Connection.Metadata["telegram_account"] || !telegramTool(tool.Name, tool.Effect) || (tool.Effect == WriteEffect && environment["TELEGRAM_WRITE"] != "true")) {
		return BackendResult{}, ErrUnauthorized
	}
	var wipe func() error
	if len(environment) > 0 {
		var err error
		wipe, err = writeAuthorizedFiles(ctx, b.Root, effective, environment)
		if err != nil {
			return BackendResult{}, err
		}
		if wipe != nil {
			defer wipe()
		}
	}
	var receipt AdmissionReceipt
	if effective.Definition.Transport == ContainerMCP {
		if err := validateToolHivePolicy(effective.Definition); err != nil {
			return BackendResult{}, err
		}
		if b.AdmissionVerifier == nil && b.Admission == nil {
			return BackendResult{}, ErrIsolation
		}
		if b.AdmissionVerifier != nil {
			var err error
			receipt, err = b.AdmissionVerifier(ctx, effective)
			if err != nil {
				return BackendResult{}, fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
			}
		} else if err := b.Admission(ctx, effective); err != nil {
			return BackendResult{}, fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
		} else {
			return BackendResult{}, fmt.Errorf("%w: workload controller returned no enforcement proof", ErrIsolation)
		}
	}
	endpoint := ""
	if effective.Connection != nil {
		endpoint = effective.Connection.Metadata["mcp_endpoint"]
	}
	if endpoint == "" {
		endpoint = receipt.Endpoint
	}
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
	// Serena 1.5.x and other established MCP servers speak the latest legacy
	// protocol. ToolHub only needs request/response calls, so pin this backend
	// hop to that version instead of letting the SDK's newer handshake break
	// otherwise compatible servers.
	baseTransport = legacyMCPRoundTripper{base: baseTransport}
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
	out := BackendResult{Text: text.String(), Structured: result.StructuredContent, IsError: result.IsError}
	if telegram && tool.Effect == WriteEffect && !result.IsError {
		raw, err := json.Marshal(result.StructuredContent)
		var receipt struct {
			Receipt   string `json:"receipt"`
			PeerID    int64  `json:"peer_id"`
			MessageID int64  `json:"message_id"`
			PTS       int64  `json:"pts"`
		}
		if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.PeerID <= 0 || receipt.MessageID <= 0 {
			return BackendResult{}, fmt.Errorf("telegram mutation returned no provider receipt")
		}
		if fmt.Sprint(arguments["peer_id"]) != fmt.Sprint(receipt.PeerID) || (tool.Name == "delete_message" && fmt.Sprint(arguments["target_id"]) != fmt.Sprint(receipt.MessageID)) {
			return BackendResult{}, ErrInvalid
		}
		want := fmt.Sprintf("%s:%d:%d", environment["TELEGRAM_ACCOUNT_ID"], receipt.PeerID, receipt.MessageID)
		if tool.Name == "delete_message" {
			if receipt.PTS <= 0 {
				return BackendResult{}, ErrInvalid
			}
			want += fmt.Sprintf(":pts:%d", receipt.PTS)
		}
		if receipt.Receipt != want {
			return BackendResult{}, ErrInvalid
		}
		out.Receipt = want
	}
	return out, nil
}

type legacyMCPRoundTripper struct{ base http.RoundTripper }

func (t legacyMCPRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("MCP-Protocol-Version", legacyMCPVersion)
	return t.base.RoundTrip(copyRequest)
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
