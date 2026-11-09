package toolhub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	DefaultEndpointPath = "/mcp"
	RunEndpointPath     = "/mcp-run"
	maxGatewayBodyBytes = 4 << 20
	legacyMCPVersion    = "2025-11-25"
)

type Gateway struct {
	Store   *Store
	Backend api.ToolBackend
	// acquireRuntime warms and holds an agent executor outside the registry fence.
	acquireRuntime             func(context.Context, api.EffectiveBinding) (func(), error)
	Tokens                     map[string]identity.Envelope
	primaryToken               string
	tokensFile                 string
	mu                         sync.Mutex
	DisableLocalhostProtection bool
	Audit                      func(event string, fields map[string]string)
	AuditWrite                 func(event string, fields map[string]string) error
	Injector                   api.CredentialInjector
	// OAuthRefresh restores a token only after a trusted backend proves setup
	// authentication failed before tools/call dispatch. It never retries an
	// uncertain business operation. Nil disables this setup recovery.
	OAuthRefresh func(context.Context, api.EffectiveBinding) (bool, error)
	Control      *ControlPlane
	// governance is the host-owned tool policy document (issue 139). Nil
	// keeps the legacy projection behavior; a configured but unreadable
	// document fails every projection closed.
	Governance *GovernanceStore
	// ControlToken is the control-plane bearer (supervisor auth) allowed to
	// read a principal's connector inventory via X-Hub-Principal. Principal
	// bearers always pin their own envelope and never use the header.
	ControlToken string

	// SpacePrincipal is this deployment's space user id: control-token
	// /v1/rights/decide calls may only assert it — cross-space assertions
	// refuse (SPEC-0092 R2.2). Empty disables asserted decisions.
	SpacePrincipal string
	// PassportRoot contains one read-only published revision per principal.
	// Memory admission resolves it from the authenticated envelope only.
	PassportRoot string
	// InboxRoots is the control-only writable inbox mount per principal.
	InboxRoots       string
	CommunicationURL string
	ModelHTTP        *http.Client
	// Vaults resolves a verified principal/environment to that principal's
	// vault through the server-rendered bindings (HUB_VAULTS_BY_PRINCIPAL).
	// Nil when no bindings root is rendered — memory stays unavailable.
	Vaults             *VaultResolver
	proposalAfterWrite func(int) error // fault seam for interrupted multi-file commits
	// Runs is the lifecycle-authority seam (supervisor /v1/jobs) consulted by
	// the exact-run gate for model-facing memory calls. Nil fails closed.
	Runs api.RunAuthority
	// CLIRelease drops bounded-cli cells for the caller's principal; the
	// /v1/cli-jobs/release route uses it for job-end task-cell teardown.
	CLIRelease  func(context.Context, api.CliReleaseRequest) error
	projections map[string]*gatewayProjection
	// adminMu serializes diagnostics calls; adminLast paces them (issue 141).
	adminMu   sync.Mutex
	adminLast time.Time
	// runCalls bounds tool fan-out per admitted run: the count is in-memory —
	// a restart forgives the budget, which matches the per-run contract.
	runCalls map[string]int
	runCap   int
}

type gatewayProjection struct {
	auth       identity.Envelope
	server     *mcp.Server
	handler    http.Handler
	runHandler http.Handler
	mu         sync.Mutex
	tools      map[string]api.ToolSpec
	controls   map[string]bool
}

func (g *Gateway) Handler() (http.Handler, error) {
	if g == nil || g.Store == nil || g.Backend == nil || len(g.Tokens) == 0 {
		return nil, fmt.Errorf("%w: gateway dependencies", api.ErrInvalid)
	}
	for token, auth := range g.Tokens {
		if len(token) < 32 || auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion) != nil {
			return nil, fmt.Errorf("%w: gateway token identity", api.ErrInvalid)
		}
	}
	g.projections = make(map[string]*gatewayProjection, len(g.Tokens))
	for token, auth := range g.Tokens {
		server, tools, controls, err := g.projectedServer(auth)
		if err != nil {
			if errors.Is(err, api.ErrUnauthorized) {
				// One enrolled identity whose managed scope no longer resolves
				// (e.g. a sibling-space capability profile this store does not
				// hold) must not take down the shared endpoint. The token gets
				// no projection, so requests to it stay denied — fail closed
				// per principal, matching the refresh-path tolerance.
				log.Printf("toolhub projection skipped principal_id=%s error=%v", auth.PrincipalID, err)
				continue
			}
			return nil, err
		}
		g.projections[token] = &gatewayProjection{
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
	}
	mux := http.NewServeMux()
	mux.Handle(DefaultEndpointPath, g.protect(DefaultEndpointPath))
	mux.Handle(RunEndpointPath, g.protect(RunEndpointPath))
	mux.Handle("/v1/connectors", http.HandlerFunc(g.serveConnectors))
	mux.Handle("/v1/connectors:install", http.HandlerFunc(g.serveConnectorInstall))
	mux.Handle("/v1/connectors/", http.HandlerFunc(g.serveConnectorVerb))
	mux.Handle("/v1/operations/", http.HandlerFunc(g.serveOperation))
	mux.Handle("/v1/confirmations", http.HandlerFunc(g.serveHumanConfirmation))
	mux.Handle("/v1/tool-call", http.HandlerFunc(g.serveToolCall))
	mux.Handle("/v1/inbound-fetch", http.HandlerFunc(g.serveInboundFetch))
	mux.Handle("/v1/cli-jobs/release", http.HandlerFunc(g.serveCLIJobRelease))
	mux.Handle("/v1/memory-capture", http.HandlerFunc(g.serveMemoryCapture))
	mux.Handle("/v1/memory-context", http.HandlerFunc(g.serveMemoryContext))
	mux.Handle("/v1/rights/decide", http.HandlerFunc(g.serveRightsDecide))
	mux.Handle("/v1/admin/onboardings", http.HandlerFunc(g.serveAdminOnboardings))
	mux.Handle("/v1/admin/diagnose/", http.HandlerFunc(g.serveAdminDiagnose))
	mux.Handle("/v1/admin/inspect-source", http.HandlerFunc(g.serveAdminInspectSource))
	mux.Handle("/knowledge", http.HandlerFunc(g.serveKnowledge))
	mux.Handle("/credentials/", http.HandlerFunc(g.serveCredentials))
	mux.Handle("/confirm/", http.HandlerFunc(g.serveConfirm))
	mux.Handle("/remove/", http.HandlerFunc(g.serveRemove))
	mux.Handle("/connect/", http.HandlerFunc(g.serveConnect))
	mux.Handle("/viewer/", http.HandlerFunc(g.serveViewer))
	mux.Handle("/device/", http.HandlerFunc(g.serveDevice))
	mux.Handle("/grants/", http.HandlerFunc(g.serveGrantAck))
	mux.Handle("/oauth/callback", http.HandlerFunc(g.serveOAuthCallback))
	return mux, nil
}

// PublicMux is the opt-in public listener surface (HUB_PUBLIC_ORIGIN): only
// human-action pages are registered — control and gateway paths do not exist
// here at all. Requests arriving on it carry a marker that lets them past the
// loopback gate.
func (g *Gateway) PublicMux() *http.ServeMux {
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(withPublicSurface(r.Context())))
		}
	}
	mux.Handle("/credentials/", wrap(g.serveCredentials))
	mux.Handle("/confirm/", wrap(g.serveConfirm))
	mux.Handle("/remove/", wrap(g.serveRemove))
	mux.Handle("/connect/", wrap(g.serveConnect))
	mux.Handle("/viewer/", wrap(g.serveViewer))
	mux.Handle("/device/", wrap(g.serveDevice))
	mux.Handle("/oauth/callback", wrap(g.serveOAuthCallback))
	mux.Handle("/knowledge", wrap(g.serveKnowledge))
	return mux
}

// ConnectorView is the read-only per-principal inventory for /connections:
// the same data tools/list projects, grouped by definition, without the MCP
// handshake. Token auth resolves the envelope exactly like the MCP endpoint.
type ConnectorView struct {
	DefinitionID string   `json:"definition_id"`
	Version      string   `json:"version"`
	Transport    string   `json:"transport"`
	Tools        []string `json:"tools"`
}

func (g *Gateway) serveConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	auth, ok := g.connectorAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tools, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	if tools, err = g.filterProjectedTools(auth, tools); err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	byDefinition := map[string][]string{}
	var order []string
	versions := map[string]string{}
	for _, t := range tools {
		if _, seen := byDefinition[t.DefinitionID]; !seen {
			order = append(order, t.DefinitionID)
		}
		byDefinition[t.DefinitionID] = append(byDefinition[t.DefinitionID], t.Name)
		versions[t.DefinitionID] = t.Version
	}
	slices.Sort(order)
	views := make([]ConnectorView, 0, len(order))
	for _, id := range order {
		view := ConnectorView{DefinitionID: id, Version: versions[id], Tools: byDefinition[id]}
		slices.Sort(view.Tools)
		if def, derr := g.Store.Definition(id, view.Version); derr == nil {
			view.Transport = string(def.Transport)
		}
		views = append(views, view)
	}
	inventory, err := (&ControlPlane{Store: g.Store}).connectorList(auth)
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connectors": views, "inventory": inventory.Candidates})
}

// connectorFacadeResult runs one connector_* verb through the shared facade
// for the authenticated envelope and writes the uniform OperationResult.
func (g *Gateway) connectorFacadeResult(w http.ResponseWriter, r *http.Request, tool string, args map[string]any) {
	if g.Control == nil {
		http.Error(w, "control plane unavailable", http.StatusServiceUnavailable)
		return
	}
	auth, ok := g.connectorAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	result, err := g.Control.InvokeConnector(r.Context(), auth, tool, args)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, api.ErrUnauthorized) {
			status = http.StatusForbidden
		} else if errors.Is(err, api.ErrNotFound) || errors.Is(err, api.ErrInvalid) {
			status = http.StatusNotFound
		}
		http.Error(w, "connector operation failed", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// serveConnectorInstall is POST /v1/connectors:install — the control-plane
// surface for the connector_install verb, same facade as the agent tool.
func (g *Gateway) serveConnectorInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGatewayBodyBytes))
		if decoder.Decode(&args) != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
	}
	g.connectorFacadeResult(w, r, "connector_install", args)
}

// serveConnectorVerb maps enable and disable HTTP actions onto the same
// connector facade verbs as the agent tools.
func (g *Gateway) serveConnectorVerb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/connectors/")
	id, tool := "", ""
	if value, found := strings.CutSuffix(path, ":enable"); found {
		id, tool = value, "connector_enable"
	} else if value, found := strings.CutSuffix(path, ":disable"); found {
		id, tool = value, "connector_disable"
	}
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	g.connectorFacadeResult(w, r, tool, map[string]any{"target": id})
}

// serveOperation is GET /v1/operations/{id} — the journaled operation record
// through the same connector_status facade verb.
func (g *Gateway) serveOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.Header.Get("Origin") != "" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/operations/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	g.connectorFacadeResult(w, r, "connector_status", map[string]any{"operation_id": id})
}

// serveToolCall is the deterministic-probe path: the same auth as the
// connector inventory, but it invokes one projected tool through the audited
// call fences and returns its flattened text.
func (g *Gateway) serveToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	auth, ok := g.connectorAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, err := g.requireActivated(auth); err != nil {
		http.Error(w, "activation unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Tool == "" {
		http.Error(w, "invalid tool call", http.StatusBadRequest)
		return
	}
	corr := api.HeaderCorrelation(r.Header.Get(api.HeaderJobID), r.Header.Get(api.HeaderRunID))
	result, err := g.callReadTool(api.WithCallCorrelation(r.Context(), corr), auth, body.Tool, body.Arguments)
	if err != nil {
		if errors.Is(err, errProbeNotProjected) {
			http.Error(w, "tool is not a projected read tool", http.StatusForbidden)
			return
		}
		if errors.Is(err, errProbeRegistry) {
			http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
			return
		}
		if data := uncertainCallData(err); data != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(data)
			return
		}
		http.Error(w, "tool call failed", http.StatusBadGateway)
		return
	}
	value := flattenToolText(result)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"text": string(value), "is_error": result.IsError})
}

var errProbeNotProjected = errors.New("tool is not a projected read tool")
var errProbeRegistry = errors.New("tool registry unavailable")

func (g *Gateway) callReadTool(ctx context.Context, auth identity.Envelope, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	tools, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errProbeRegistry, err)
	}
	tools, err = g.filterProjectedTools(auth, tools)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errProbeRegistry, err)
	}
	for _, tool := range tools {
		if tool.Name == name && tool.Tool.Effect == api.ReadEffect {
			return g.call(ctx, auth, name, arguments)
		}
	}
	return nil, errProbeNotProjected
}

func flattenToolText(result *mcp.CallToolResult) []byte {
	var value strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			if value.Len() > 0 {
				value.WriteByte('\n')
			}
			value.WriteString(text.Text)
		}
	}
	return []byte(value.String())
}

// serveCLIJobRelease is the job-end hook: the communication hub calls it when
// a job reaches a terminal status so task-tier cells keyed by that job die
// with the job rather than waiting out the idle TTL. Auth pins the principal
// exactly like /v1/connectors — a caller can only release its own jobs.
func (g *Gateway) serveCLIJobRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins forbidden", http.StatusForbidden)
		return
	}
	auth, ok := g.connectorAuth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || !identity.ValidID(body.JobID) {
		http.Error(w, "invalid job release", http.StatusForbidden)
		return
	}
	if g.CLIRelease == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := g.CLIRelease(r.Context(), api.CliReleaseRequest{PrincipalID: auth.PrincipalID, JobID: body.JobID, Reason: "job-end"}); err != nil {
		http.Error(w, "cell release failed", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// connectorAuth resolves the caller's identity for the read-only connector
// listing. A principal's own bearer always pins its envelope. The
// control-plane bearer (HUB_COMMUNICATION_AUTH — the supervisor token shared
// with communication-hub) may select any enrolled principal via
// X-Hub-Principal; the header alone never authenticates.
func (g *Gateway) connectorAuth(r *http.Request) (identity.Envelope, bool) {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return identity.Envelope{}, false
	}
	if token, ok := g.matchToken("Bearer " + value); ok {
		g.mu.Lock()
		auth := g.Tokens[token]
		g.mu.Unlock()
		return auth, true
	}
	if g.ControlToken == "" || subtle.ConstantTimeCompare([]byte(value), []byte(g.ControlToken)) != 1 {
		return identity.Envelope{}, false
	}
	principal := strings.TrimSpace(r.Header.Get("X-Hub-Principal"))
	if principal == "" {
		return identity.Envelope{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, auth := range g.Tokens {
		if auth.PrincipalID == principal {
			return auth, true
		}
	}
	return identity.Envelope{}, false
}

func (g *Gateway) protect(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins forbidden", http.StatusForbidden)
			return
		}
		token, ok := g.authenticate(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		projection, err := g.projectionFor(token)
		if err != nil {
			http.Error(w, "projection unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := g.refreshProjection(projection); err != nil {
			http.Error(w, "projection unavailable", http.StatusServiceUnavailable)
			return
		}
		corr := api.HeaderCorrelation(r.Header.Get(api.HeaderJobID), r.Header.Get(api.HeaderRunID))
		r = r.WithContext(api.WithCallCorrelation(r.Context(), corr))
		if path == RunEndpointPath {
			projection.runHandler.ServeHTTP(w, r)
		} else {
			projection.handler.ServeHTTP(w, r)
		}
	})
}

func (g *Gateway) authenticate(value string) (string, bool) {
	// The bearer can stay the same while its policy envelope changes after
	// a sibling rebuild. Re-read the small enrolled-token file so a cached
	// projection cannot keep the old authority.
	if g.tokensFile != "" {
		g.reloadTokens()
	}
	return g.matchToken(value)
}

func (g *Gateway) matchToken(value string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var found identity.Envelope
	var foundToken string
	matched := false
	for token, auth := range g.Tokens {
		if subtle.ConstantTimeCompare([]byte(value), []byte("Bearer "+token)) == 1 {
			if matched {
				return "", false
			}
			foundToken, found, matched = token, auth, true
		}
	}
	if !matched || found.Validate(found.PrincipalID, found.ContextID, found.RuntimeID, found.PolicyVersion) != nil {
		return "", false
	}
	return foundToken, true
}

func (g *Gateway) reloadTokens() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tokensFile == "" {
		return false
	}
	extra, err := loadTokenEnvelopes(g.tokensFile)
	if err != nil {
		log.Printf("toolhub token reload skipped: %v", err)
		if primaryAuth, ok := g.Tokens[g.primaryToken]; ok {
			g.Tokens = map[string]identity.Envelope{g.primaryToken: primaryAuth}
			g.projections = nil
		}
		return false
	}
	primaryAuth, ok := g.Tokens[g.primaryToken]
	if !ok || g.primaryToken == "" {
		return false
	}
	next := map[string]identity.Envelope{g.primaryToken: primaryAuth}
	for token, auth := range extra {
		if token == g.primaryToken {
			continue
		}
		if _, dup := next[token]; dup {
			log.Printf("toolhub token reload skipped a duplicate bearer")
			continue
		}
		next[token] = auth
	}
	for token, old := range g.Tokens {
		if current, ok := next[token]; !ok || !envelopeEqual(old, current) {
			delete(g.projections, token)
		}
	}
	g.Tokens = next
	return true
}
