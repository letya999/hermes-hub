package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (g *Gateway) addControlTools(server *mcp.Server, auth identity.Envelope) {
	if g == nil || g.Control == nil || server == nil {
		return
	}
	schema := map[string]any{"type": "object", "additionalProperties": true}
	for _, op := range ControlOperations {
		name := op
		server.AddTool(&mcp.Tool{Name: name, Description: "ToolHub control operation " + name + ". Identity is taken from the authenticated request; owner, locator, backend and policy arguments are rejected.", InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			if request != nil && len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("%w: tool arguments: %v", ErrInvalid, err)
				}
			}
			body, err := g.Control.Invoke(ctx, auth, name, arguments)
			if err != nil {
				return nil, err
			}
			if name == "required_credentials" {
				if pending := pendingCredentialElicit(ctx, request, body); pending != nil {
					return pending, nil
				}
			}
			encoded, _ := json.Marshal(body)
			return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
		})
	}
}

const mcpProtocol20260728 = "2026-07-28"

func pendingCredentialElicit(ctx context.Context, request *mcp.CallToolRequest, body map[string]any) *mcp.CallToolResult {
	formURL, _ := body["form_url"].(string)
	if formURL == "" || request == nil || !clientSupportsURLElicitation(request) {
		return nil
	}
	if request.Params != nil && len(request.Params.InputResponses) > 0 {
		return nil
	}
	params := &mcp.ElicitParams{Mode: "url", Message: "Enter credentials on the protected loopback form.", URL: formURL}
	// 2026-07-28 forbids standalone Session.Elicit during tools/call; URL
	// elicitation must travel as InputRequests so the client receives form_url.
	if request.ProtocolVersion() >= mcpProtocol20260728 {
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"credentials": params}}
	}
	if request.Session != nil {
		_, _ = request.Session.Elicit(ctx, params)
	}
	return nil
}

func clientSupportsURLElicitation(request *mcp.CallToolRequest) bool {
	if request == nil {
		return false
	}
	caps := request.ClientCapabilities()
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

func (g *Gateway) serveCredentials(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/credentials/")
	onboardingID := strings.Trim(path, "/")
	if onboardingID == "" || strings.Contains(onboardingID, "/") {
		http.NotFound(w, r)
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if r.Method == http.MethodGet {
		onboarding, err := g.Control.Store.onboarding(onboardingID)
		if err != nil || onboarding.FormNonce == "" || nonce != onboarding.FormNonce {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		var b strings.Builder
		b.WriteString("<!doctype html><title>ToolHub credentials</title><form method=\"post\">")
		b.WriteString("<input type=\"hidden\" name=\"nonce\" value=\"" + html.EscapeString(onboarding.FormNonce) + "\">")
		for _, hint := range onboarding.Required {
			b.WriteString("<label>" + html.EscapeString(hint.Name) + "<input type=\"password\" name=\"" + html.EscapeString(hint.Name) + "\" autocomplete=\"off\"></label>")
		}
		b.WriteString("<button type=\"submit\">store</button></form>")
		_, _ = io.WriteString(w, b.String())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if nonce == "" {
		nonce = r.Form.Get("nonce")
	}
	values := map[string]string{}
	for key, vs := range r.PostForm {
		if key == "nonce" || len(vs) == 0 {
			continue
		}
		values[key] = vs[0]
	}
	if err := g.Control.SubmitCredentials(onboardingID, nonce, values); err != nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"stored"}`)
}

func (g *Gateway) serveOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil || g.Control.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	onboardingID := r.URL.Query().Get("onboarding_id")
	if onboardingID == "" {
		http.Error(w, "missing onboarding", http.StatusBadRequest)
		return
	}
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	redirect := g.Control.origin() + "/oauth/callback?onboarding_id=" + url.QueryEscape(onboardingID)
	if err := g.Control.HandleOAuthCallback(auth, onboardingID, r.URL.Query().Get("state"), r.URL.Query().Get("code"), redirect); err != nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"stored"}`)
}

func loopbackHTTP(r *http.Request) bool {
	if r == nil {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
