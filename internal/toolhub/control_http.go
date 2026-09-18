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
	for _, op := range ControlOperations {
		name := op
		description, schema := controlToolContract(name)
		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			if request != nil && len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("%w: tool arguments: %v", ErrInvalid, err)
				}
			}
			notifyControlProgress(ctx, request, name, false, nil)
			body, err := g.Control.Invoke(ctx, auth, name, arguments)
			if err != nil {
				return nil, err
			}
			notifyControlProgress(ctx, request, name, true, body)
			if name == "required_credentials" {
				if pending := pendingCredentialElicit(ctx, request, body); pending != nil {
					return pending, nil
				}
			}
			encoded, _ := json.Marshal(body)
			return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
		})
	}
	server.AddTool(&mcp.Tool{Name: "invoke", Description: "Call an enabled ToolHub projected tool without reconnecting the MCP client. Pass the exact projected tool name and its arguments.", InputSchema: map[string]any{
		"type": "object", "required": []string{"tool"}, "additionalProperties": false,
		"properties": map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}},
	}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if request == nil || json.Unmarshal(request.Params.Arguments, &arguments) != nil || strings.TrimSpace(arguments.Tool) == "" {
			return nil, fmt.Errorf("%w: invoke arguments", ErrInvalid)
		}
		return g.call(ctx, auth, arguments.Tool, arguments.Arguments)
	})
}

func controlToolContract(name string) (string, map[string]any) {
	properties := map[string]any{
		"onboarding_id": map[string]any{"type": "string", "description": "Exact onboarding_id returned by prepare_source."},
		"definition_id": map[string]any{"type": "string", "description": "Connector definition ID. Use this to resume an earlier onboarding when its onboarding_id is unknown."},
		"version":       map[string]any{"type": "string"},
	}
	description := "ToolHub control operation " + name + ". Identity is taken from the authenticated request; owner, locator, backend and policy arguments are rejected."
	switch name {
	case "prepare_source":
		description += " For every explicit install/add request containing a GitHub repository URL, call this first with that URL in source, even if chat history mentions an older installation. Do not call remove, revoke or status first."
	case "disable", "revoke", "remove":
		description += " Call this only when the user's current message explicitly requests this lifecycle action; never use it to prepare or retry an install."
	case "status", "required_credentials":
		description += " Pass onboarding_id, or pass definition_id to resume the latest onboarding for that connector."
		if name == "status" {
			description += " With no selector, status returns the latest onboarding for the authenticated user."
		}
	}
	return description, map[string]any{"type": "object", "properties": properties, "additionalProperties": true}
}

func notifyControlProgress(ctx context.Context, request *mcp.CallToolRequest, op string, done bool, body map[string]any) {
	if request == nil || request.Params == nil || request.Session == nil {
		return
	}
	token := request.Params.GetProgressToken()
	if token == nil {
		return
	}
	progress, message := 0.1, "Task accepted"
	switch op {
	case "prepare_source":
		message = "Source is being verified and built"
		if done {
			progress, message = 1, "Source review and build completed"
		}
	case "required_credentials":
		message = "Credentials or OAuth are required"
		if done {
			progress = 1
		}
	case "confirm":
		message = "Credentials accepted"
		if done {
			progress = 1
		}
	case "enable":
		message = "MCP is starting"
		if done {
			progress, message = 1, "MCP is ready"
		}
	default:
		if done {
			progress, message = 1, "ToolHub operation completed"
		}
	}
	if phase, _ := body["phase"].(string); done && phase == PhaseFailed {
		message = "MCP installation failed"
	}
	_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: progress, Total: 1, Message: message})
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
		if err != nil || onboarding.Phase != PhaseAwaitingCreds || onboarding.FormNonce == "" || nonce != onboarding.FormNonce || onboarding.FormExpires.IsZero() || g.Control.now().After(onboarding.FormExpires) {
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
	_, _ = io.WriteString(w, `{"status":"stored","phase":"awaiting-confirm"}`)
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
	_, _ = io.WriteString(w, `{"status":"ready"}`)
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
