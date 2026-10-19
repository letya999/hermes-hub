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
	maxGatewayBodyBytes = 4 << 20
	legacyMCPVersion    = "2025-11-25"
)

type Gateway struct {
	Store                      *Store
	Backend                    api.ToolBackend
	Tokens                     map[string]identity.Envelope
	primaryToken               string
	tokensFile                 string
	mu                         sync.Mutex
	DisableLocalhostProtection bool
	Audit                      func(event string, fields map[string]string)
	AuditWrite                 func(event string, fields map[string]string) error
	Injector                   api.CredentialInjector
	// OAuthRefresh mints a new access token for a connection whose remote
	// call was rejected, then reports whether a retry may carry a fresh
	// bearer. Nil disables refresh-retry.
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
	// CLIRelease drops bounded-cli cells for the caller's principal; the
	// /v1/cli-jobs/release route uses it for job-end task-cell teardown.
	CLIRelease  func(context.Context, api.CliReleaseRequest) error
	projections map[string]*gatewayProjection
}

type gatewayProjection struct {
	auth     identity.Envelope
	server   *mcp.Server
	handler  http.Handler
	mu       sync.Mutex
	tools    map[string]api.ToolSpec
	controls map[string]bool
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
		}
	}
	mux := http.NewServeMux()
	mux.Handle(DefaultEndpointPath, g.protect())
	mux.Handle("/v1/connectors", http.HandlerFunc(g.serveConnectors))
	mux.Handle("/v1/tool-call", http.HandlerFunc(g.serveToolCall))
	mux.Handle("/v1/cli-jobs/release", http.HandlerFunc(g.serveCLIJobRelease))
	mux.Handle("/credentials/", http.HandlerFunc(g.serveCredentials))
	mux.Handle("/confirm/", http.HandlerFunc(g.serveConfirm))
	mux.Handle("/remove/", http.HandlerFunc(g.serveRemove))
	mux.Handle("/connect/", http.HandlerFunc(g.serveConnect))
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
	mux.Handle("/device/", wrap(g.serveDevice))
	mux.Handle("/oauth/callback", wrap(g.serveOAuthCallback))
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connectors": views})
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
	tools, err := g.Store.ListProjectedTools(auth)
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	if tools, err = g.filterProjectedTools(auth, tools); err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	allowed := false
	for _, tool := range tools {
		if tool.Name == body.Tool && tool.Tool.Effect == api.ReadEffect {
			allowed = true
		}
	}
	if !allowed {
		http.Error(w, "tool is not a projected read tool", http.StatusForbidden)
		return
	}
	result, err := g.call(r.Context(), auth, body.Tool, body.Arguments)
	if err != nil {
		http.Error(w, "tool call failed", http.StatusBadGateway)
		return
	}
	var text strings.Builder
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(tc.Text)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"text": text.String(), "is_error": result.IsError})
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

func (g *Gateway) protect() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultEndpointPath {
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
		projection.handler.ServeHTTP(w, r)
	})
}

func (g *Gateway) authenticate(value string) (string, bool) {
	if token, ok := g.matchToken(value); ok {
		return token, true
	}
	// A sibling space enrolled after process start is invisible until the
	// tokens file is read again. Reload only on a miss so the steady path
	// stays the in-memory map.
	if g.reloadTokens() {
		return g.matchToken(value)
	}
	return "", false
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
	g.Tokens = next
	return true
}
