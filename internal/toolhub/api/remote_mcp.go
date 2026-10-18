package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type remoteProbeResult struct {
	Tools      []ToolSpec
	AuthNeeded bool
	// Scopes carries the WWW-Authenticate challenge scope on a 401 — the
	// resource's own demand, which outranks PRM scopes_supported.
	Scopes []string
}

// ProbeRemoteMCP proves the endpoint speaks MCP: a real initialize handshake
// plus a bounded tools/list. The returned tool set is capped and classified —
// provider names and descriptions are untrusted content, never instructions.
func ProbeRemoteMCP(ctx context.Context, definition ToolDefinition, env map[string]string) (remoteProbeResult, error) {
	endpoint := definition.Source.URL
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return remoteProbeResult{}, err
	}
	headers, err := CredentialHeaders(definition, env)
	if err != nil {
		return remoteProbeResult{}, err
	}
	client := RemoteHTTPClient(endpoint, headers)
	var lastStatus int
	var challenge string
	inner := client.Transport
	client.Transport = RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := inner.RoundTrip(request)
		if response != nil {
			lastStatus = response.StatusCode
			if lastStatus == http.StatusUnauthorized {
				challenge = response.Header.Get("WWW-Authenticate")
			}
		}
		return response, err
	})
	session, err := connectRemoteMCP(ctx, endpoint, client)
	if err != nil {
		return remoteProbeResult{AuthNeeded: lastStatus == http.StatusUnauthorized || lastStatus == http.StatusForbidden, Scopes: challengeScopes(challenge)}, fmt.Errorf("%w: remote MCP handshake: %v", ErrUnauthorized, sanitizeRemoteError(err))
	}
	defer session.Close()
	var tools []ToolSpec
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return remoteProbeResult{AuthNeeded: lastStatus == http.StatusUnauthorized || lastStatus == http.StatusForbidden}, fmt.Errorf("%w: remote MCP tools/list: %v", ErrUnauthorized, sanitizeRemoteError(err))
		}
		if tool == nil || !McpToolNamePattern.MatchString(tool.Name) {
			continue
		}
		spec := ToolSpec{Name: tool.Name, Effect: RemoteAnnotatedEffect(tool)}
		if tool.Description != "" {
			spec.Description = tool.Description
			if len(spec.Description) > 1024 {
				spec.Description = spec.Description[:1024]
			}
		}
		if tool.InputSchema != nil {
			schema, mErr := json.Marshal(tool.InputSchema)
			if mErr != nil || len(schema) > 65536 || !json.Valid(schema) {
				return remoteProbeResult{}, fmt.Errorf("%w: remote tool %q schema", ErrInvalid, tool.Name)
			}
			spec.InputSchema = schema
		}
		tools = append(tools, spec)
		if len(tools) > 256 {
			return remoteProbeResult{}, fmt.Errorf("%w: remote MCP tool list is unbounded", ErrInvalid)
		}
	}
	if len(tools) == 0 {
		return remoteProbeResult{}, fmt.Errorf("%w: remote MCP listed no tools", ErrInvalid)
	}
	slices.SortFunc(tools, func(a, b ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	return remoteProbeResult{Tools: tools}, nil
}

// challengeParam reads one Bearer WWW-Authenticate parameter (RFC6750 §3);
// the scheme word is dropped first so it cannot contaminate the first key.
func challengeParam(header, name string) string {
	rest := header
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		rest = rest[i+1:]
	}
	for _, part := range strings.Split(rest, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

// challengeScopes returns the challenge's scope= demand — space-separated
// inside quotes.
func challengeScopes(header string) []string {
	return strings.Fields(challengeParam(header, "scope"))
}

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// connectRemoteMCP opens a session over the current streamable transport and
// falls back to the legacy 2024-11-05 SSE transport when the endpoint does
// not speak it. Hosted MCP servers still ship SSE-only; the SSE client goes
// through the same SSRF-guarded dialer and origin-pinned headers.
func connectRemoteMCP(ctx context.Context, endpoint string, client *http.Client) (*mcp.ClientSession, error) {
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-remote", Version: "0.2.0"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client}, nil)
	if err == nil {
		return session, nil
	}
	streamableErr := err
	session, err = mcpClient.Connect(ctx, &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: client}, nil)
	if err != nil {
		return nil, streamableErr // report the current-protocol failure, not the fallback's
	}
	return session, nil
}

// sanitizeRemoteError strips URLs and credential-shaped material out of probe
// failures; a denial reason must never echo a token-bearing endpoint string.
func sanitizeRemoteError(err error) string {
	text := secretValuePattern.ReplaceAllString(err.Error(), "[redacted]")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

var remoteWriteVerbs = map[string]bool{
	"create": true, "update": true, "delete": true, "send": true, "post": true,
	"put": true, "patch": true, "remove": true, "write": true, "merge": true,
	"close": true, "reopen": true, "assign": true, "comment": true, "add": true,
	"set": true, "move": true, "rename": true, "execute": true, "run": true,
	"trigger": true, "invite": true, "upload": true, "fork": true, "star": true,
	"follow": true, "push": true, "commit": true, "reply": true, "cancel": true,
	"approve": true, "reject": true, "schedule": true, "deploy": true,
	"publish": true, "subscribe": true, "notify": true, "transfer": true,
	"provision": true, "destroy": true, "edit": true, "insert": true,
}

// RemoteAnnotatedEffect fails closed: only an explicit readOnlyHint=true
// annotation (without destructiveHint) classifies a remote tool read.
func RemoteAnnotatedEffect(tool *mcp.Tool) Effect {
	if tool != nil && tool.Annotations != nil && tool.Annotations.ReadOnlyHint &&
		!(tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint) {
		return ReadEffect
	}
	return WriteEffect
}

// RemoteToolEffect classifies on name segments only — provider text is
// untrusted and a verb heuristic is a deny-safe default, not a promise.
func RemoteToolEffect(name string) Effect {
	for _, segment := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if remoteWriteVerbs[segment] {
			return WriteEffect
		}
	}
	return ReadEffect
}

// RemoteDefinitionBase builds the immutable manifest skeleton for an
// owner-scoped remote endpoint. Tools stay empty until a real MCP probe
// produces them — a draft is never a tool contract.
func RemoteDefinitionBase(endpoint, name string, credentials []CredentialInput) (ToolDefinition, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ToolDefinition{}, fmt.Errorf("%w: remote MCP URL", ErrInvalid)
	}
	id := strings.TrimSpace(name)
	if id == "" {
		id = "remote-" + slug(u.Hostname())
	}
	if len(id) > 40 || !identity.ValidID(id) || !strings.HasPrefix(id, "remote-") {
		return ToolDefinition{}, fmt.Errorf("%w: remote definition id must be a remote-* id", ErrInvalid)
	}
	egress := []string{strings.ToLower(u.Hostname())}
	if port := u.Port(); port != "" {
		egress = []string{strings.ToLower(u.Hostname()) + ":" + port}
	}
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: id, Version: "1.0.0", Transport: RemoteMCP,
		Source:      DefinitionSource{URL: "https://" + u.Host + u.EscapedPath(), TLSMode: "required"},
		Credentials: credentials,
		Workload:    WorkloadPolicy{Class: PerUser, Rationale: "owner-scoped remote MCP endpoint; session and credentials belong to one principal"},
		Execution:   ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 8, Egress: egress},
		Health:      HealthProbe{Kind: "http", Value: "/", TimeoutSeconds: 5},
	}, nil
}

// SelectRemoteTools narrows an advertised set to the requested allowlist; an
// allowlist can never widen what the endpoint actually serves.
func SelectRemoteTools(tools []ToolSpec, allowlist []string) ([]ToolSpec, error) {
	if len(allowlist) == 0 {
		return tools, nil
	}
	known := map[string]ToolSpec{}
	for _, tool := range tools {
		known[tool.Name] = tool
	}
	selected := make([]ToolSpec, 0, len(allowlist))
	for _, want := range allowlist {
		tool, ok := known[want]
		if !ok {
			return nil, fmt.Errorf("%w: allowlisted tool %q was not advertised", ErrInvalid, want)
		}
		selected = append(selected, tool)
	}
	return selected, nil
}

// RemoteMCPDefinition builds the immutable owner-scoped manifest for a probed
// endpoint. The tool allowlist may narrow the advertised set but never widen
// it; every selected tool is recorded with its discovered schema and effect.
func RemoteMCPDefinition(endpoint, name string, credentials []CredentialInput, tools []ToolSpec, allowlist []string) (ToolDefinition, error) {
	definition, err := RemoteDefinitionBase(endpoint, name, credentials)
	if err != nil {
		return ToolDefinition{}, err
	}
	selected, err := SelectRemoteTools(tools, allowlist)
	if err != nil {
		return ToolDefinition{}, err
	}
	if len(selected) == 0 {
		return ToolDefinition{}, fmt.Errorf("%w: remote MCP tool allowlist is empty", ErrInvalid)
	}
	definition.Tools = selected
	return definition, definition.Validate()
}

// RemoteCredentialInputs turns prepare_source credential arguments into
// declared inputs. Every remote credential is delivered as an HTTP header;
// "bearer" is the common case, an explicit target covers API-key headers.
func RemoteCredentialInputs(args map[string]any) ([]CredentialInput, error) {
	var inputs []CredentialInput
	seen := map[string]bool{}
	raw, ok := args["credentials"]
	if !ok || raw == nil {
		return nil, nil
	}
	var list []any
	switch typed := raw.(type) {
	case []any:
		list = typed
	case []string:
		for _, name := range typed {
			list = append(list, name)
		}
	default:
		return nil, fmt.Errorf("%w: credentials must be a list", ErrInvalid)
	}
	if len(list) > 8 {
		return nil, fmt.Errorf("%w: too many remote credentials", ErrInvalid)
	}
	header := ArgString(args, "credential_header")
	globalPrefix, hasPrefix := args["credential_prefix"].(string)
	for _, item := range list {
		var input CredentialInput
		switch typed := item.(type) {
		case string:
			input = CredentialInput{Name: strings.TrimSpace(typed), Required: true, Delivery: "http_header"}
		case map[string]any:
			input = CredentialInput{
				Name:     ArgString(typed, "name"),
				Required: true,
				Delivery: "http_header",
				Target:   ArgString(typed, "header"),
				Prefix:   ArgString(typed, "prefix"),
			}
		default:
			return nil, fmt.Errorf("%w: invalid remote credential entry", ErrInvalid)
		}
		if input.Target == "" {
			input.Target = header
		}
		if input.Target == "" {
			input.Target = "Authorization"
		}
		if input.Prefix == "" {
			if hasPrefix {
				input.Prefix = globalPrefix
			} else if strings.EqualFold(input.Target, "authorization") {
				input.Prefix = "Bearer "
			}
		}
		if !CredentialPattern.MatchString(input.Name) || seen[input.Name] {
			return nil, fmt.Errorf("%w: invalid or duplicate credential %q", ErrInvalid, input.Name)
		}
		if err := validateHeaderDelivery(input); err != nil {
			return nil, err
		}
		seen[input.Name] = true
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// RemotePlaceholders records the requested allowlist on the draft definition
// so the credentialed admission probe knows which advertised tools to keep.
// Draft tools are never registered or projected.
func RemotePlaceholders(allowlist []string) []ToolSpec {
	if len(allowlist) == 0 {
		return nil
	}
	tools := make([]ToolSpec, 0, len(allowlist))
	for _, name := range allowlist {
		tools = append(tools, ToolSpec{Name: name, Effect: ReadEffect})
	}
	return tools
}

func RemoteCredentialHints(credentials []CredentialInput) []CredentialHint {
	hints := make([]CredentialHint, 0, len(credentials))
	for _, input := range credentials {
		hints = append(hints, CredentialHint{
			Name: input.Name, Type: "secret", Secret: true,
			Delivery: "http_header", Target: input.Target,
			Hint: "protected loopback form",
		})
	}
	return hints
}

// remoteDefinitionEndpoint reports whether the endpoint lives on the
// immutable definition itself (owner-registered remote MCP) rather than in
// connection metadata (vMCP/aggregate backends pinned by the operator). A
// Google-Workspace connector also carries its endpoint on the connection, so
// it keeps the dedicated backend path.
func remoteDefinitionEndpoint(effective EffectiveBinding) bool {
	if strings.HasPrefix(effective.Definition.DefinitionID, "google-workspace-") {
		return false
	}
	return effective.Connection == nil || effective.Connection.Metadata["mcp_endpoint"] == ""
}
