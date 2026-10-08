package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// User-owned remote MCP endpoints are the only ToolHub traffic allowed to
// leave the private network. Everything here fails closed: HTTPS only, a
// public destination proven by DNS at validate time and again at dial time,
// no redirects, and a real MCP handshake — an HTTP 200 that does not answer
// initialize/tools/list is not MCP.

var (
	// resolveRemoteIP is the DNS seam shared by validation and dialing. Tests
	// pin it; production uses the system resolver.
	resolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	// remoteHTTPClient builds the endpoint-bound HTTP client. Tests substitute
	// an httptest client; production gets the SSRF-guarded transport.
	remoteHTTPClient  = defaultRemoteHTTPClient
	headerNamePattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)
)

// remoteDenyPrefixes covers address space that is reachable-but-not-public:
// CGNAT (RFC 6598), benchmarking (RFC 2544), 6to4 relay and the protocol
// assignment blocks a resolver should never legitimately return for a remote
// MCP host. Loopback/private/link-local/unspecified/multicast are denied by
// the classifier below on top of this list.
var remoteDenyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("233.252.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::ffff:0:0/96").Masked(),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("fec0::/10"),
}

func remoteAddrPublic(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range remoteDenyPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateRemoteMCPEndpoint admits only an HTTPS URL whose host is a public,
// non-reserved destination. Private backends (ToolHive/vMCP/compose names,
// loopback, link-local, metadata ranges) and single-label service names are
// operator config, never user chat input.
func ValidateRemoteMCPEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("%w: remote MCP URL must be HTTPS without credentials, query or fragment", ErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		// IP literals bypass the name rules entirely and stand on the
		// public-address check alone.
		if !remoteAddrPublic(ip) {
			return fmt.Errorf("%w: remote MCP host resolves to a private address", ErrUnauthorized)
		}
		return nil
	}
	if host == "" || !strings.Contains(host, ".") {
		return fmt.Errorf("%w: remote MCP host must be a public name", ErrUnauthorized)
	}
	switch host {
	case "localhost", "toolhub", "toolhive", "vmcp", "workload-controller", "host.docker.internal", "metadata.google.internal":
		return fmt.Errorf("%w: remote MCP host is a private backend name", ErrUnauthorized)
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".corp") || strings.HasSuffix(host, ".lan") {
		return fmt.Errorf("%w: remote MCP host is a private backend name", ErrUnauthorized)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return checkRemoteHost(ctx, host)
}

func checkRemoteHost(ctx context.Context, host string) error {
	ips, err := resolveRemoteIP(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%w: remote MCP host does not resolve", ErrUnauthorized)
	}
	for _, ip := range ips {
		if !remoteAddrPublic(ip) {
			return fmt.Errorf("%w: remote MCP host resolves to a private address", ErrUnauthorized)
		}
	}
	return nil
}

// remoteDial resolves the host itself and refuses to connect when every
// address is not public — the dial-time re-check behind the validate-time
// DNS answer (rebinding window).
func remoteDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = resolveRemoteIP(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	public := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if remoteAddrPublic(ip) {
			public = append(public, ip)
		}
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("%w: remote MCP dial to a non-public address", ErrUnauthorized)
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var lastErr error
	for _, ip := range public {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type remoteHeaderRoundTripper struct {
	base    http.RoundTripper
	origin  string // "https://host[:port]" the credential headers belong to
	headers map[string]string
}

// RoundTrip injects credential headers only on requests to the admitted
// origin. Legacy SSE endpoints get a server-chosen message URL — a hostile
// server could point it at a different origin and exfiltrate the headers, so
// anything off-origin travels unauthenticated.
func (t remoteHeaderRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	req := request
	if len(t.headers) > 0 && req.URL != nil && requestOrigin(req.URL) == t.origin {
		req = request.Clone(request.Context())
		for name, value := range t.headers {
			req.Header.Set(name, value)
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &remoteLimitedBody{r: io.LimitReader(resp.Body, remoteMaxBodyBytes), c: resp.Body}
	return resp, nil
}

func requestOrigin(u *url.URL) string {
	port := u.Port()
	host := u.Hostname()
	if port != "" && port != "443" {
		return u.Scheme + "://" + host + ":" + port
	}
	return u.Scheme + "://" + host
}

// remoteMaxBodyBytes bounds every HTTP response the remote client will
// buffer: the go-sdk decodes bodies with unbounded io.ReadAll, so the cap has
// to live in transport — far below memory pressure, far above the largest
// legitimate tools/list or call result (OutputBytes governs those).
const remoteMaxBodyBytes = 8 << 20

type remoteLimitedBody struct {
	r io.Reader
	c io.Closer
}

func (b *remoteLimitedBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *remoteLimitedBody) Close() error               { return b.c.Close() }

// defaultRemoteHTTPClient binds the SSRF guard into transport and redirect
// policy. A remote MCP endpoint never legitimately redirects: following one
// would let a reviewed URL launder a private or alternate destination.
func defaultRemoteHTTPClient(endpoint string, headers map[string]string) *http.Client {
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           remoteDial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	origin := ""
	if u, err := url.Parse(endpoint); err == nil {
		origin = requestOrigin(u)
	}
	return &http.Client{
		Timeout:       45 * time.Second,
		Transport:     remoteHeaderRoundTripper{base: base, origin: origin, headers: headers},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CredentialHeaders maps admitted credential values onto the HTTP headers the
// immutable definition declares. Only inputs with delivery "http_header"
// produce headers; a required one missing from the injected environment fails
// closed instead of silently authenticating nothing.
func CredentialHeaders(definition ToolDefinition, env map[string]string) (map[string]string, error) {
	headers := map[string]string{}
	for _, input := range definition.Credentials {
		if input.Delivery != "http_header" {
			continue
		}
		value, ok := env[input.Name]
		if !ok || value == "" {
			if input.Required {
				return nil, fmt.Errorf("%w: credential %s not injected", ErrUnauthorized, input.Name)
			}
			continue
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%w: credential %s cannot be a header", ErrInvalid, input.Name)
		}
		headers[input.Target] = input.Prefix + value
	}
	return headers, nil
}

type remoteProbeResult struct {
	Tools      []ToolSpec
	AuthNeeded bool
}

// probeRemoteMCP proves the endpoint speaks MCP: a real initialize handshake
// plus a bounded tools/list. The returned tool set is capped and classified —
// provider names and descriptions are untrusted content, never instructions.
func probeRemoteMCP(ctx context.Context, definition ToolDefinition, env map[string]string) (remoteProbeResult, error) {
	endpoint := definition.Source.URL
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return remoteProbeResult{}, err
	}
	headers, err := CredentialHeaders(definition, env)
	if err != nil {
		return remoteProbeResult{}, err
	}
	client := remoteHTTPClient(endpoint, headers)
	var lastStatus int
	inner := client.Transport
	client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := inner.RoundTrip(request)
		if response != nil {
			lastStatus = response.StatusCode
		}
		return response, err
	})
	session, err := connectRemoteMCP(ctx, endpoint, client)
	if err != nil {
		return remoteProbeResult{AuthNeeded: lastStatus == http.StatusUnauthorized || lastStatus == http.StatusForbidden}, fmt.Errorf("%w: remote MCP handshake: %v", ErrUnauthorized, sanitizeRemoteError(err))
	}
	defer session.Close()
	var tools []ToolSpec
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return remoteProbeResult{AuthNeeded: lastStatus == http.StatusUnauthorized || lastStatus == http.StatusForbidden}, fmt.Errorf("%w: remote MCP tools/list: %v", ErrUnauthorized, sanitizeRemoteError(err))
		}
		if tool == nil || !mcpToolNamePattern.MatchString(tool.Name) {
			continue
		}
		spec := ToolSpec{Name: tool.Name, Effect: remoteToolEffect(tool.Name)}
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

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

// remoteToolEffect classifies on name segments only — provider text is
// untrusted and a verb heuristic is a deny-safe default, not a promise.
func remoteToolEffect(name string) Effect {
	for _, segment := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if remoteWriteVerbs[segment] {
			return WriteEffect
		}
	}
	return ReadEffect
}

// remoteDefinitionBase builds the immutable manifest skeleton for an
// owner-scoped remote endpoint. Tools stay empty until a real MCP probe
// produces them — a draft is never a tool contract.
func remoteDefinitionBase(endpoint, name string, credentials []CredentialInput) (ToolDefinition, error) {
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

// selectRemoteTools narrows an advertised set to the requested allowlist; an
// allowlist can never widen what the endpoint actually serves.
func selectRemoteTools(tools []ToolSpec, allowlist []string) ([]ToolSpec, error) {
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
	definition, err := remoteDefinitionBase(endpoint, name, credentials)
	if err != nil {
		return ToolDefinition{}, err
	}
	selected, err := selectRemoteTools(tools, allowlist)
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
	header := argString(args, "credential_header")
	globalPrefix, hasPrefix := args["credential_prefix"].(string)
	for _, item := range list {
		var input CredentialInput
		switch typed := item.(type) {
		case string:
			input = CredentialInput{Name: strings.TrimSpace(typed), Required: true, Delivery: "http_header"}
		case map[string]any:
			input = CredentialInput{
				Name:     argString(typed, "name"),
				Required: true,
				Delivery: "http_header",
				Target:   argString(typed, "header"),
				Prefix:   argString(typed, "prefix"),
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
		if !credentialPattern.MatchString(input.Name) || seen[input.Name] {
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

func validateHeaderDelivery(input CredentialInput) error {
	if input.Delivery != "http_header" {
		return fmt.Errorf("%w: remote credential %s must deliver as an HTTP header", ErrInvalid, input.Name)
	}
	if !headerNamePattern.MatchString(input.Target) || len(input.Target) > 64 || strings.EqualFold(input.Target, "host") || strings.EqualFold(input.Target, "content-length") {
		return fmt.Errorf("%w: invalid credential header %q", ErrInvalid, input.Target)
	}
	if len(input.Prefix) > 32 || strings.ContainsAny(input.Prefix, "\r\n\x00") {
		return fmt.Errorf("%w: invalid credential header prefix", ErrInvalid)
	}
	return nil
}

// remotePlaceholders records the requested allowlist on the draft definition
// so the credentialed admission probe knows which advertised tools to keep.
// Draft tools are never registered or projected.
func remotePlaceholders(allowlist []string) []ToolSpec {
	if len(allowlist) == 0 {
		return nil
	}
	tools := make([]ToolSpec, 0, len(allowlist))
	for _, name := range allowlist {
		tools = append(tools, ToolSpec{Name: name, Effect: ReadEffect})
	}
	return tools
}

func remoteCredentialHints(credentials []CredentialInput) []CredentialHint {
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

// prepareRemote registers an owner-scoped remote MCP endpoint for a granted
// principal. The URL is validated as a real HTTPS MCP destination before any
// store write. A credential-free endpoint is probed immediately; one that
// declares credentials defers the probe to the protected form submit, so the
// definition is published only after the endpoint proves itself under the
// owner's own credential.
func (c *ControlPlane) prepareRemote(ctx context.Context, auth identity.Envelope, endpoint string, args map[string]any) (map[string]any, error) {
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return nil, err
	}
	credentials, err := RemoteCredentialInputs(args)
	if err != nil {
		return nil, err
	}
	name := argString(args, "name")
	allowlist := argStrings(args, "tools")
	for _, tool := range allowlist {
		if !mcpToolNamePattern.MatchString(tool) {
			return nil, fmt.Errorf("%w: invalid tool allowlist entry %q", ErrInvalid, tool)
		}
	}
	draft, err := remoteDefinitionBase(endpoint, name, credentials)
	if err != nil {
		return nil, err
	}
	requestKey := argString(args, "request_key")
	seed := requestKey
	if seed == "" {
		credKey := make([]string, 0, len(credentials))
		for _, input := range credentials {
			credKey = append(credKey, input.Name+">"+input.Target)
		}
		seed = "remote-mcp:" + draft.DefinitionID + "@" + draft.Version + ":" + draft.Source.URL + ":" + strings.Join(credKey, ",") + ":" + strings.Join(allowlist, ",")
	}
	onboardingID := deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, seed)
	if existing, err := c.Store.onboarding(onboardingID); err == nil && existing.Phase != PhaseFailed && existing.Phase != PhaseRemoved {
		if err := c.refreshCredentialForm(&existing); err != nil {
			return nil, err
		}
		if err := c.refreshConfirmation(&existing); err != nil {
			return nil, err
		}
		return c.statusBody(existing, false), nil
	}
	if len(credentials) > 0 {
		draft.Tools = remotePlaceholders(allowlist)
		onboarding := Onboarding{
			Schema: SchemaVersion, OnboardingID: onboardingID,
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			Mode: OnboardingRemote, Phase: PhaseAwaitingCreds, SourceURL: draft.Source.URL,
			DefinitionID: draft.DefinitionID, DefinitionVersion: draft.Version,
			Required: remoteCredentialHints(credentials), AdmissionPending: true,
			Error:     "The remote endpoint needs credentials before tools/list. Submit them through the protected form; it is checked against the endpoint and the tool list is recorded.",
			FormNonce: randomNonce(), FormExpires: c.now().Add(c.ttl()),
			IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
			Definition: &draft,
		}
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return nil, err
		}
		return c.statusBody(onboarding, false), nil
	}
	probe, err := probeRemoteMCP(ctx, draft, nil)
	if err != nil {
		if probe.AuthNeeded {
			return nil, fmt.Errorf("%w: remote endpoint requires credentials; resubmit prepare_source with a credentials list", ErrUnauthorized)
		}
		return nil, err
	}
	definition, err := RemoteMCPDefinition(endpoint, name, nil, probe.Tools, allowlist)
	if err != nil {
		return nil, err
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, OnboardingRemote, requestKey, definition, definition.Source.URL, "")
	if err != nil {
		return nil, err
	}
	if onboarding.OnboardingID != onboardingID {
		onboarding.OnboardingID = onboardingID
	}
	copyDef := definition
	onboarding.Definition = &copyDef
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// admitRemoteDefinition re-probes a credentialed remote endpoint with the
// owner-submitted secrets mapped onto the declared HTTP headers. The
// definition's placeholder tools become the allowlist; the advertised set may
// narrow but never widen it.
func admitRemoteDefinition(ctx context.Context, definition ToolDefinition, secrets map[string]string) (ToolDefinition, error) {
	if definition.Transport != RemoteMCP {
		return ToolDefinition{}, fmt.Errorf("%w: remote admission needs a remote-mcp draft", ErrInvalid)
	}
	allowlist := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		allowlist = append(allowlist, tool.Name)
	}
	draft := definition
	draft.Tools = nil
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := probeRemoteMCP(probeCtx, draft, secrets)
	if err != nil {
		return ToolDefinition{}, err
	}
	admitted := definition
	admitted.Tools, err = selectRemoteTools(probe.Tools, allowlist)
	if err != nil {
		return ToolDefinition{}, err
	}
	if len(admitted.Tools) == 0 {
		return ToolDefinition{}, fmt.Errorf("%w: remote MCP tool allowlist is empty", ErrInvalid)
	}
	return admitted, admitted.Validate()
}

// remoteReady proves the endpoint is still an MCP server and still advertises
// every bound tool before a binding goes active.
func (b MCPBackend) remoteReady(ctx context.Context, effective EffectiveBinding, environment map[string]string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probe, err := probeRemoteMCP(probeCtx, effective.Definition, environment)
	if err != nil {
		return fmt.Errorf("%w: remote MCP readiness: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	advertised := map[string]bool{}
	for _, tool := range probe.Tools {
		advertised[tool.Name] = true
	}
	for _, tool := range effective.Definition.Tools {
		if !advertised[tool.Name] {
			return fmt.Errorf("%w: remote endpoint no longer serves %q", ErrStale, tool.Name)
		}
	}
	return nil
}

// callRemoteMCP projects one tools/call onto the owner's remote endpoint. The
// endpoint is re-validated at call time and credentials arrive only as the
// declared HTTP headers — no ToolHive admission, no credential files.
func (b MCPBackend) callRemoteMCP(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	endpoint := effective.Definition.Source.URL
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		return BackendResult{}, err
	}
	headers, err := CredentialHeaders(effective.Definition, environment)
	if err != nil {
		return BackendResult{}, err
	}
	session, err := connectRemoteMCP(ctx, endpoint, remoteHTTPClient(endpoint, headers))
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP session: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	closeErr := session.Close()
	if err != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP call: %v", ErrIsolation, sanitizeRemoteError(err))
	}
	if closeErr != nil {
		return BackendResult{}, fmt.Errorf("%w: remote MCP close: %v", ErrIsolation, sanitizeRemoteError(closeErr))
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	return BackendResult{Text: text.String(), Content: result.Content, Structured: result.StructuredContent, IsError: result.IsError}, nil
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
