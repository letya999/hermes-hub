package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (g *Gateway) addControlTools(server *mcp.Server, auth identity.Envelope, allowed map[string]bool) {
	if g == nil || g.Control == nil || server == nil {
		return
	}
	for _, tool := range api.ConnectorTools {
		if !allowed[tool] {
			continue
		}
		name := tool
		description, schema := connectorToolContract(name)
		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			if request != nil && len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("%w: tool arguments: %v", api.ErrInvalid, err)
				}
			}
			result, err := g.Control.InvokeConnector(ctx, auth, name, arguments)
			if err != nil {
				return nil, err
			}
			encoded, _ := json.Marshal(result)
			return &mcp.CallToolResult{StructuredContent: result, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
		})
	}
	for _, op := range api.ControlOperations {
		if !allowed[op] {
			continue
		}
		name := op
		description, schema := controlToolContract(name)
		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			arguments := map[string]any{}
			if request != nil && len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("%w: tool arguments: %v", api.ErrInvalid, err)
				}
			}
			notifyControlProgress(ctx, request, name, false, nil)
			body, err := g.Control.Invoke(ctx, auth, name, arguments)
			if err != nil {
				return nil, err
			}
			notifyControlProgress(ctx, request, name, true, body)
			if name == "required_credentials" || (name == "confirm" || name == "status") && body["phase"] == api.PhaseAwaitingOAuth {
				if pending := pendingCredentialElicit(ctx, request, body, g.Control); pending != nil {
					return pending, nil
				}
			}
			encoded, _ := json.Marshal(body)
			return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil
		})
	}
	if !allowed["invoke"] {
		return
	}
	server.AddTool(&mcp.Tool{Name: "invoke", Description: "Call an enabled ToolHub projected tool without reconnecting the MCP client. Pass the exact projected tool name and its arguments.", InputSchema: map[string]any{
		"type": "object", "required": []string{"tool"}, "additionalProperties": false,
		"properties": map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}},
	}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := g.Store.RequireControlOperation(auth, "invoke"); err != nil {
			return nil, err
		}
		var arguments struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if request == nil || json.Unmarshal(request.Params.Arguments, &arguments) != nil || strings.TrimSpace(arguments.Tool) == "" {
			return nil, fmt.Errorf("%w: invoke arguments", api.ErrInvalid)
		}
		return g.call(ctx, auth, arguments.Tool, arguments.Arguments)
	})
}

func controlToolContract(name string) (string, map[string]any) {
	properties := map[string]any{
		"onboarding_id": map[string]any{"type": "string", "description": "Exact onboarding_id returned by prepare_source."},
		"definition_id": map[string]any{"type": "string", "description": "Connector definition ID. Use this to resume an earlier onboarding when its onboarding_id is unknown."},
		"selector":      map[string]any{"type": "string", "description": "Conversational connector name: a definition_id, the source repository URL or its basename (for example search-mcp). Use when the user names a connector instead of quoting an id. Refuses ambiguous matches; retry with the source URL or onboarding_id."},
		"name":          map[string]any{"type": "string", "description": "Alias of selector."},
		"source":        map[string]any{"type": "string", "description": "Alias of selector — pass the exact source repository URL."},
		"operation_id":  map[string]any{"type": "string", "description": "Request key from the original install call; resolves the matching onboarding for status checks."},
		"version":       map[string]any{"type": "string"},
	}
	description := "ToolHub control operation " + name + ". Identity is taken from the authenticated request; owner, locator, backend and policy arguments are rejected."
	switch name {
	case "discover":
		properties = map[string]any{"query": map[string]any{"type": "string", "maxLength": 256}}
		description += " Read-only search: returns a prepared repository and up to four matching registry alternatives. Select its candidate_id with prepare_source; discovery does not install."
	case "prepare_source":
		properties["connection_recipe"] = map[string]any{"type": "object", "description": "Optional reviewed connection recipe (fields and OAuth endpoints, scopes, client sections, token template, state target and read probe). Contains no credentials. Secrets are collected only in the protected form."}
		properties["source"] = map[string]any{"type": "string", "description": "GitHub repository URL of a hosted MCP server connector. To install a command-line tool (ripgrep, bat, jq, fd, …) put a cli spec object in cli instead — never a bare repo URL here."}
		properties["candidate_id"] = map[string]any{"type": "string"}
		properties["request_key"] = map[string]any{"type": "string"}
		properties["credential_source"] = map[string]any{"type": "string", "description": "Authoritative metadata file for the credential recipe when sources disagree (for example server.json or .env.example). The status credential_sources list names the discovered choices; replay prepare_source with the same request_key to switch sources on the existing onboarding."}
		properties["remote_url"] = map[string]any{"type": "string", "description": "Public HTTPS remote MCP endpoint for a user-owned connector (for example https://api.githubcopilot.com/mcp/). Only the endpoint's official URL on the user's explicit request; private, IP-literal, redirecting or non-MCP targets are refused."}
		properties["name"] = map[string]any{"type": "string", "description": "Optional remote-* connector id derived from the endpoint host when omitted."}
		properties["tools"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional allowlist of advertised remote tool names to expose; omit to expose every advertised tool."}
		properties["scope"] = map[string]any{"type": "string", "enum": []string{"owner", "shared"}, "description": "owner (default) scopes the connector to the caller. shared is operator-only (install_shared grant): the connector becomes catalog-visible and every member binds one shared connection and credential via a shared-credential policy — for endpoints where one service account serves everyone."}
		properties["credentials"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Credential names to collect through the protected form (for example GITHUB_TOKEN). Each becomes an Authorization: Bearer header unless it names its own header."}
		properties["telegram_send"] = map[string]any{"type": "boolean", "description": "Only on the user's explicit request to enable sending: true with the enabled Telegram onboarding_id upgrades that owner's existing session to reads plus send_message. No new login. Then confirm the returned nonce and enable."}
		properties["cli"] = map[string]any{"type": "object", "description": "Only on the user's explicit request for an owner-scoped bounded CLI: a declarative spec with name, command (an executable from the operator allowlist — shell names are admitted only when the operator put them there), optional args (long-option tokens only), tools (name/effect plus typed arguments; a --flag name renders a flag, omitting flag renders a positional operand that then requires a pattern regexp), optional credentials and egress. With source plus binary instead of command, onboards an immutable artifact: default to github-release:owner/repo@latest — popular tools (bat, fd, jq, eza, sd, yq, fzf, age) all ship prebuilt linux-amd64 assets (asset is a glob on the release assets, e.g. *x86_64-unknown-linux-musl.tar.gz or jq-linux-amd64 for a bare binary — on a miss the error lists available asset names; member names the binary inside an archive and defaults to the tool name; digest optional). Use a commit-pinned GitHub URL only after the release path fails, since source builds cover just a single-package Go or Rust layout. Never a command string, path or script."}
		description += " For every explicit install/add request for a hosted MCP server containing a GitHub repository URL, call this first with that URL in source, even if chat history mentions an older installation. For an explicit request to add a personal command-line tool, pass the declarative spec in cli — not the source field. Do not call remove, revoke or status first. Review and build may outlive the call: on phase=preparing poll status with the returned onboarding_id. If the user only asks to resend the protected link or asks how an earlier install is doing — not for a fresh install — call status or required_credentials with the connector's definition_id instead: it resumes the latest onboarding and reissues a live link when the old one expired, without rebuilding. For an explicit request naming a hosted remote MCP endpoint, pass that official URL in remote_url instead of source."
		cli := properties["cli"].(map[string]any)
		cli["properties"], cli["required"], cli["additionalProperties"] = cliInstallProperties(), []string{"name", "tools"}, false
		cli["oneOf"] = []any{
			map[string]any{"required": []string{"source", "binary"}, "properties": cliRequiredProperties("source", "binary"), "not": map[string]any{"required": []string{"command"}, "properties": cliRequiredProperties("command")}},
			map[string]any{"required": []string{"command"}, "properties": cliRequiredProperties("command"), "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"source"}, "properties": cliRequiredProperties("source")}, map[string]any{"required": []string{"binary"}, "properties": cliRequiredProperties("binary")}}}},
		}
		cli["examples"] = []any{map[string]any{"name": "jq", "source": "github-release:jqlang/jq@latest", "asset": "jq-linux-amd64", "binary": "/usr/local/bin/jq", "args": []string{"--version"}, "tools": []any{map[string]any{"name": "version", "effect": "read"}}}}
	case "rotate", "disable", "revoke", "remove":
		description += " Call this only when the user's current message explicitly requests this lifecycle action; never use it to prepare or retry an install."
		if name == "remove" {
			properties["remove_nonce"] = map[string]any{"type": "string", "description": "Confirmation token from the previous remove preview response."}
			description += " Remove is destructive: the first call returns a preview with remove_nonce; ask the user to confirm, then call remove again with that remove_nonce."
		}
	case "diagnostics":
		properties = map[string]any{
			"tail":     map[string]any{"type": "integer", "description": "Newest lines to return, 1-500 (default 200).", "minimum": 1, "maximum": 500},
			"since":    map[string]any{"type": "string", "description": "RFC3339 lower time bound."},
			"until":    map[string]any{"type": "string", "description": "RFC3339 upper time bound."},
			"search":   map[string]any{"type": "string", "description": "Case-insensitive substring filter.", "maxLength": 256},
			"severity": map[string]any{"type": "string", "enum": []string{"info", "warn", "error"}},
			"workload": map[string]any{"type": "string", "description": "Narrow to a workload_id, binding_id, definition_id, or \"runtime\" for the Hermes runtime container."},
		}
		description = "User-scoped runtime diagnostics. Returns health for the authenticated caller's own connector workloads plus bounded, redacted log lines from their runtime and workload containers. Host logs, other users' data, secrets and raw Docker APIs are never exposed."
	case "grant_request":
		properties = map[string]any{
			"section":  map[string]any{"type": "string", "description": "Governance section to unlock; default user_mcp."},
			"grant_id": map[string]any{"type": "string", "description": "Exact grant_id when the host named one."},
		}
		description = "Request activation of a host-created tool governance grant. Call only when the user's current message explicitly asks to enable a denied tool section. Returns a protected acknowledge URL — the user opens it to activate the grant; the URL alone does not grant anything."
	case "status", "required_credentials":
		description += " Pass onboarding_id, definition_id, a selector/name/source naming the connector, or the operation_id (request_key) of the original call."
		if name == "status" {
			properties["resend"] = map[string]any{"type": "boolean", "description": "Only when the user explicitly asks for the pending action link again (send/resend/re-issue the link): posts it as a fresh channel message. For an OAuth onboarding the armed link is rotated — the previous link stops working, so do not resend as a routine poll."}
			description += " With no selector, status returns the latest onboarding for the authenticated user."
		}
	}
	return description, map[string]any{"type": "object", "properties": properties, "additionalProperties": true}
}

// Pinned Hermes prunes required names absent from their local properties map.
// Declare selector fields in each conditional so its normalization preserves XOR.
func cliRequiredProperties(names ...string) map[string]any {
	properties := map[string]any{}
	for _, name := range names {
		properties[name] = map[string]any{"type": "string"}
	}
	return properties
}

func cliInstallProperties() map[string]any {
	properties := map[string]any{}
	for key := range cliSpecKeys {
		properties[key] = map[string]any{"type": "string"}
	}
	for _, key := range []string{"args", "credentials", "egress"} {
		properties[key] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 64}
	}
	argument := map[string]any{"type": "object", "required": []string{"name"}, "additionalProperties": false, "properties": map[string]any{
		"name": map[string]any{"type": "string"}, "flag": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"string", "integer", "number", "boolean"}},
		"required": map[string]any{"type": "boolean"}, "pattern": map[string]any{"type": "string"},
	}}
	properties["tools"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{"type": "object", "required": []string{"name", "effect"}, "additionalProperties": false, "properties": map[string]any{
		"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "effect": map[string]any{"type": "string", "enum": []string{"read", "write"}},
		"arguments": map[string]any{"type": "array", "maxItems": 32, "items": argument},
	}}}
	properties["runtime_env"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	properties["stateless"] = map[string]any{"type": "boolean"}
	properties["install_packages"] = map[string]any{"type": "boolean", "description": "Private executable home; requires operator cli_registries approval. Never shares Hermes home. Use binding lifecycle to retain installs between calls."}
	properties["source"] = map[string]any{"type": "string", "description": "Prefer github-release:owner/repo@tag (or @latest), with asset glob and absolute guest binary path. Commit-pinned GitHub source builds are a separate fallback."}
	properties["binary"] = map[string]any{"type": "string", "pattern": "^/", "description": "Executable path inside the verified image, e.g. /usr/local/bin/jq; not a host path."}
	properties["scope"] = map[string]any{"type": "string", "enum": []string{"owner", "shared"}}
	return properties
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
	if phase, _ := body["phase"].(string); done && phase == api.PhaseFailed {
		message = "MCP installation failed"
	}
	_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: progress, Total: 1, Message: message})
}

const mcpProtocol20260728 = "2026-07-28"

// pendingCredentialElicit resolves the protected action link server-side: the
// model-facing body only carries hints (input/phase), so the URL is rebuilt
// from the stored onboarding and reaches the user through the MCP client's own
// URL-elicitation UI — never through result text. Clients without URL
// elicitation get the hint body untouched; the channel notice remains the
// fallback link path.
func pendingCredentialElicit(ctx context.Context, request *mcp.CallToolRequest, body map[string]any, control *ControlPlane) *mcp.CallToolResult {
	if request == nil || control == nil || control.Store == nil {
		return nil
	}
	onboardingID, _ := body["onboarding_id"].(string)
	if onboardingID == "" {
		return nil
	}
	if body["input"] != "localhost-form" && body["phase"] != api.PhaseAwaitingOAuth {
		return nil
	}
	onboarding, err := control.Store.onboarding(onboardingID)
	if err != nil {
		return nil
	}
	actionURL := api.ActionURL(control.origin(), onboarding)
	if actionURL == "" {
		return nil
	}
	if request.Params != nil && len(request.Params.InputResponses) > 0 {
		return nil
	}
	message := "Open the protected credential form."
	if body["phase"] == api.PhaseAwaitingOAuth {
		message = "Open the protected credential or OAuth URL."
	}
	if !clientSupportsURLElicitation(request) {
		return nil
	}
	params := &mcp.ElicitParams{Mode: "url", Message: message, URL: actionURL}
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
	nonce := linkToken(r)
	if r.Method == http.MethodGet {
		onboarding, err := g.Control.Store.onboarding(onboardingID)
		if err != nil || onboarding.Phase != api.PhaseAwaitingCreds || onboarding.FormNonce == "" || nonce != onboarding.FormNonce || onboarding.FormExpires.IsZero() || g.Control.now().After(onboarding.FormExpires) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cspNonce := api.RandomNonce()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+cspNonce+"'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = io.WriteString(w, g.credentialFormHTML(onboarding, onboardingID, "", nil, cspNonce, pageLang(r)))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	g.submitCredentialForm(w, r, onboardingID, nonce)
}

// submitCredentialForm runs the credential POST pipeline shared by the
// one-time /credentials/ page and the stable /connect/<id>?k= wizard: parse,
// collect fields and file-upload credentials, then authorize — the stored
// form nonce still gates the write.
func (g *Gateway) submitCredentialForm(w http.ResponseWriter, r *http.Request, onboardingID, nonce string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(64 << 10)
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if nonce == "" {
		nonce = r.Form.Get("nonce")
	}
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	values := map[string]string{}
	for key, vs := range r.PostForm {
		if key == "nonce" || strings.HasPrefix(key, "google_oauth_") || len(vs) == 0 {
			continue
		}
		values[key] = vs[0]
		for _, value := range vs {
			if strings.TrimSpace(value) != "" {
				values[key] = value
			}
		}
	}
	// File-delivery fields arrive as multipart parts, not form values; the
	// uploaded content is the credential.
	for _, hint := range onboarding.Required {
		if hint.Delivery != "file" || values[hint.Name] != "" {
			continue
		}
		file, _, ferr := r.FormFile(hint.Name)
		if ferr == http.ErrMissingFile {
			continue
		}
		if ferr != nil {
			http.Error(w, "invalid file upload", http.StatusBadRequest)
			return
		}
		data, ferr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		_ = file.Close()
		if ferr != nil || len(data) > 64<<10 {
			http.Error(w, "invalid file upload", http.StatusBadRequest)
			return
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			values[hint.Name] = string(data)
		}
	}
	applyCredentialDefaults(onboarding.Required, values)
	lang := pageLang(r)
	if err := g.Control.AuthorizeCredentials(r.Context(), onboardingID, nonce, values, lang); err != nil {
		if errors.Is(err, errCredentialProbe) {
			// Transactional save: the probe rejected before any persistence,
			// so the owner retries in place — the re-rendered form carries
			// their values and the public probe error, page-side only.
			cspNonce := api.RandomNonce()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+cspNonce+"'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, g.credentialFormHTML(onboarding, onboardingID, credentialProbeFailure(err, lang), values, cspNonce, lang))
			return
		}
		status, title, message := credentialSubmitPage(err, lang)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, credentialResultPage(title, message, lang)) // #nosec G705 -- credentialResultPage escapes title and message
		return
	}
	// Accepted — send the browser back to the stable wizard link, which now
	// renders the next step (confirm, OAuth or the enabling spinner) instead
	// of a dead-end page.
	if done, derr := g.Control.Store.onboarding(onboardingID); derr == nil &&
		done.WizardNonce != "" && g.Control.now().Before(done.WizardExpires) {
		http.Redirect(w, r, g.Control.origin()+"/connect/"+url.PathEscape(onboardingID)+"?k="+url.QueryEscape(done.WizardNonce)+"&lang="+url.QueryEscape(string(lang)), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.done-title"), locale.T(lang, "page.done-creds-body"), lang))
}

// serveConfirm is the user-facing authorization page the channel notice links
// to. The model never sees the nonce: the page re-checks it against the stored
// confirmation before running the same finishAuthorization the tool used.
func (g *Gateway) serveConfirm(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil {
		http.NotFound(w, r)
		return
	}
	onboardingID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/confirm/"), "/")
	if onboardingID == "" || strings.Contains(onboardingID, "/") {
		http.NotFound(w, r)
		return
	}
	nonce := linkToken(r)
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err == nil && onboarding.ConfirmationNonce != "" && nonce == onboarding.ConfirmationNonce && (onboarding.ConfirmationUsed || onboarding.ConfirmationExpires.IsZero() || g.Control.now().After(onboarding.ConfirmationExpires)) {
		// The link was already spent: dead link, not a forged token.
		goneLinkPage(w, pageLang(r))
		return
	}
	if err != nil || onboarding.Phase != api.PhaseAwaitingConfirm || onboarding.ConfirmationNonce == "" || nonce != onboarding.ConfirmationNonce || onboarding.ConfirmationUsed || onboarding.ConfirmationExpires.IsZero() || g.Control.now().After(onboarding.ConfirmationExpires) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, confirmPage(onboarding, pageLang(r)))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lang := pageLang(r)
	var finishErr error
	if onboarding.PendingWrites {
		finishErr = g.Control.applyConfirmedWrites(r.Context(), onboardingID)
	} else {
		finishErr = g.Control.finishAuthorization(r.Context(), onboardingID)
	}
	if finishErr != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.confirm-failed-title"), api.PublicPrepareError(finishErr), lang)) // #nosec G705 -- credentialResultPage escapes title and message
		return
	}
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.confirmed-title"), locale.T(lang, "page.confirmed-body"), lang))
}

func confirmPage(onboarding api.Onboarding, lang locale.Lang) string {
	return `<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(locale.T(lang, "page.confirm-title")) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}</style><main><h1>` + html.EscapeString(locale.T(lang, "page.confirm-h1")) + `</h1><p>` + html.EscapeString(locale.T(lang, "page.confirm-body", onboarding.DefinitionID)) + `</p><form method="post"><button type="submit">` + html.EscapeString(locale.T(lang, "page.confirm-btn")) + `</button></form></main></html>`
}

// serveRemove is the destructive twin of serveConfirm: the channel notice
// carries this link after a remove preview, and the page runs the same gated
// executor the tool would — nonce and TTL checked before any cleanup starts.
func (g *Gateway) serveRemove(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil {
		http.NotFound(w, r)
		return
	}
	onboardingID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/remove/"), "/")
	if onboardingID == "" || strings.Contains(onboardingID, "/") {
		http.NotFound(w, r)
		return
	}
	nonce := linkToken(r)
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err == nil && onboarding.RemoveNonce != "" && nonce == onboarding.RemoveNonce && (onboarding.Phase == api.PhaseRemoved || onboarding.RemoveExpires.IsZero() || g.Control.now().After(onboarding.RemoveExpires)) {
		goneLinkPage(w, pageLang(r))
		return
	}
	if err != nil || onboarding.RemoveNonce == "" || nonce != onboarding.RemoveNonce || onboarding.RemoveExpires.IsZero() || g.Control.now().After(onboarding.RemoveExpires) || onboarding.Phase == api.PhaseRemoved {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	lang := pageLang(r)
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, `<!doctype html><html lang="`+html.EscapeString(string(lang))+`"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(locale.T(lang, "page.remove-title"))+`</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#b42318;color:#fff;font:inherit;font-weight:700;cursor:pointer}</style><main><h1>`+html.EscapeString(locale.T(lang, "page.remove-h1"))+`</h1><p>`+html.EscapeString(locale.T(lang, "page.remove-body", onboarding.DefinitionID))+`</p><form method="post"><button type="submit">`+html.EscapeString(locale.T(lang, "page.remove-btn"))+`</button></form></main></html>`)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := g.Control.remove(r.Context(), onboardingEnvelope(onboarding), map[string]any{"onboarding_id": onboardingID, "remove_nonce": nonce}); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.remove-failed-title"), api.PublicPrepareError(err), lang))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.removed-title"), locale.T(lang, "page.removed-body"), lang))
}

// serveConnect is the loopback hop for external action links: the channel
// notice carries /connect/<id>?state=<oauth-state> or ?request=<broker-id>,
// the endpoint validates the parameter against the stored onboarding and only
// then redirects to the provider/broker URL. The external URL and its state
// never appear in a tool result or a channel message — only the loopback hop.
func (g *Gateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil {
		http.NotFound(w, r)
		return
	}
	onboardingID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/connect/"), "/")
	if onboardingID == "" || strings.Contains(onboardingID, "/") {
		http.NotFound(w, r)
		return
	}
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if key := r.URL.Query().Get("k"); key != "" {
		g.serveWizard(w, r, onboardingID, onboarding, key)
		return
	}
	storedState := ""
	if onboarding.ProviderAuthorizationURL != "" {
		if parsed, perr := url.Parse(onboarding.ProviderAuthorizationURL); perr == nil {
			storedState = parsed.Query().Get("state")
		}
	}
	if r.Method == http.MethodPost {
		g.serveOAuthPaste(w, r, storedState)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if state := r.URL.Query().Get("state"); state != "" && onboarding.ProviderAuthorizationURL != "" {
		if parsed, perr := url.Parse(onboarding.ProviderAuthorizationURL); perr == nil && parsed.Query().Get("state") == state {
			lang := pageLang(r)
			g.Control.rememberOAuthFlowLang(state, lang)
			// The stored flow dies with its in-memory state — hand the
			// browser to the wizard link, which re-arms a live flow
			// before rendering the provider hop.
			if r.Method == http.MethodGet && g.Control.preparedOAuthOnboardingID(state) != onboardingID && onboarding.WizardNonce != "" && g.Control.now().Before(onboarding.WizardExpires) {
				http.Redirect(w, r, g.Control.origin()+"/connect/"+url.PathEscape(onboardingID)+"?k="+url.QueryEscape(onboarding.WizardNonce)+"&lang="+url.QueryEscape(string(lang)), http.StatusSeeOther)
				return
			}
			// Interstitial instead of a bare 302: a user authorizing on
			// another device hits a dead loopback /oauth/callback there —
			// the same chat link must keep working, so the page offers
			// both the provider hop and a paste-the-final-URL field.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
			_, _ = io.WriteString(w, oauthInterstitialPage(onboarding, state, lang)) // #nosec G705 -- page values are html.EscapeString-escaped
			return
		}
	}
	if request := r.URL.Query().Get("request"); request != "" && request == onboarding.BrokerRequestID && onboarding.BrokerAuthorizationURL != "" {
		http.Redirect(w, r, onboarding.BrokerAuthorizationURL, http.StatusFound)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// serveOAuthPaste is the paste-the-redirect fallback shared by the ?state=
// connect link and the wizard's awaiting-oauth step: the posted state must
// match the onboarding's stored authorization URL AND the pasted URL's own
// state, then the shared completePreparedOAuth path re-validates
// principal/context/connection and runs the PKCE exchange exactly like the
// live /oauth/callback.
func (g *Gateway) serveOAuthPaste(w http.ResponseWriter, r *http.Request, storedState string) {
	lang := g.oauthLang(r, storedState)
	if storedState == "" || r.ParseForm() != nil || r.Form.Get("state") != storedState {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pasted, perr := url.Parse(strings.TrimSpace(r.Form.Get("redirect_url")))
	if perr != nil || pasted.Query().Get("state") != storedState || pasted.Query().Get("code") == "" {
		http.Error(w, "invalid redirect URL", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := g.Control.completePreparedOAuth(r.Context(), storedState, pasted.Query().Get("code")); err != nil {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.login-failed-title"), locale.T(lang, "page.oauth-denied-body"), lang))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.done-title"), locale.T(lang, "page.done-paste-body"), lang))
}

// oauthInterstitialPage is the provider-hop page shared by the ?state=
// connect link and the wizard's awaiting-oauth phase.
func oauthInterstitialPage(onboarding api.Onboarding, state string, lang locale.Lang) string {
	return `<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="10"><title>` + html.EscapeString(locale.T(lang, "page.oauth-title")) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}a.btn{display:block;text-align:center;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font-weight:700;text-decoration:none}details{margin-top:18px;color:#52606d}summary{cursor:pointer;font-weight:650}label{display:block;font-weight:650;margin:14px 0 0}input{display:block;width:100%;margin-top:8px;border:1px solid #aab4bf;border-radius:10px;padding:12px 14px;font:inherit}button{width:100%;margin-top:16px;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}</style><main><h1>` + html.EscapeString(locale.T(lang, "page.oauth-title")) + `</h1><p>` + html.EscapeString(locale.T(lang, "page.oauth-body")) + `</p><a class="btn" rel="noopener noreferrer" href="` + html.EscapeString(onboarding.ProviderAuthorizationURL) + `">` + html.EscapeString(locale.T(lang, "page.oauth-btn")) + `</a><details><summary>` + html.EscapeString(locale.T(lang, "page.oauth-paste")) + `</summary><p>` + html.EscapeString(locale.T(lang, "page.oauth-paste-hint")) + `</p><form method="post"><input type="hidden" name="state" value="` + html.EscapeString(state) + `"><label>` + html.EscapeString(locale.T(lang, "page.oauth-paste-label")) + `<input name="redirect_url" required placeholder="http://127.0.0.1/oauth/callback?…"></label><button type="submit">` + html.EscapeString(locale.T(lang, "page.oauth-paste-btn")) + `</button></form></details></main></html>`
}

// brokerHopPage is the wizard's awaiting-credentials render for
// broker-mediated onboarding: the values live at the broker's own form, so
// the page forwards there and meta-refreshes — a submitted request flips the
// phase and the reload lands on the confirm step of the same wizard URL.
func brokerHopPage(onboarding api.Onboarding, lang locale.Lang) string {
	return `<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="8"><title>` + html.EscapeString(locale.T(lang, "page.connect-title")) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}a.btn{display:block;text-align:center;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font-weight:700;text-decoration:none}</style><main><h1>` + html.EscapeString(locale.T(lang, "page.broker-h1")) + `</h1><p>` + html.EscapeString(locale.T(lang, "page.broker-body")) + `</p><a class="btn" rel="noopener noreferrer" href="` + html.EscapeString(onboarding.BrokerAuthorizationURL) + `">` + html.EscapeString(locale.T(lang, "page.broker-btn")) + `</a></main></html>`
}

// serveWizard renders the stable single-link setup page: ?k= carries the
// wizard nonce minted with the first credential/confirmation step, and the
// page shows whatever phase the onboarding is in.
func (g *Gateway) serveWizard(w http.ResponseWriter, r *http.Request, onboardingID string, onboarding api.Onboarding, key string) {
	if onboarding.WizardNonce == "" || key != onboarding.WizardNonce || !g.Control.now().Before(onboarding.WizardExpires) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	lang := pageLang(r)
	// A polling page reload is itself a credentials ask: pull the broker
	// request forward so a submitted form flips the page to confirm without
	// a second link or a chat round-trip. The gateway dedupes the notices
	// refreshBrokerRequest posts while the invite is alive.
	if r.Method == http.MethodGet && onboarding.Phase == api.PhaseAwaitingCreds && onboarding.BrokerRequestID != "" {
		if err := g.Control.refreshBrokerRequest(r.Context(), onboardingEnvelope(onboarding), &onboarding); err != nil {
			log.Printf("toolhub wizard %s: broker refresh: %v", onboardingID, err) // #nosec G706 -- onboarding id is store-validated before logging; error text is internal
		}
	}
	if onboarding.Phase == api.PhaseAwaitingConfirm {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, confirmPage(onboarding, lang))
			return
		}
		if r.Method == http.MethodPost {
			var finishErr error
			if onboarding.PendingWrites {
				finishErr = g.Control.applyConfirmedWrites(r.Context(), onboardingID)
			} else {
				finishErr = g.Control.finishAuthorization(r.Context(), onboardingID)
			}
			if finishErr != nil {
				http.Error(w, "confirmation failed", http.StatusBadGateway)
				return
			}
			wizardURL := g.Control.origin() + "/connect/" + url.PathEscape(onboardingID) + "?k=" + url.QueryEscape(onboarding.WizardNonce) + "&lang=" + url.QueryEscape(string(lang))
			w.Header().Set("Location", wizardURL)
			w.WriteHeader(http.StatusSeeOther)
			return
		}
	}
	if onboarding.Phase == api.PhaseAwaitingOAuth {
		storedState := ""
		if parsed, perr := url.Parse(onboarding.ProviderAuthorizationURL); perr == nil {
			storedState = parsed.Query().Get("state")
		}
		if r.Method == http.MethodPost {
			g.serveOAuthPaste(w, r, storedState)
			return
		}
		if r.Method == http.MethodGet {
			g.Control.rememberOAuthFlowLang(storedState, lang)
			// The provider hop dies with its in-memory flow (step TTL or a
			// toolhub restart): re-arm it so this page never fronts a dead
			// authorization URL that only fails at /oauth/callback.
			if g.Control.preparedOAuthOnboardingID(storedState) != onboardingID {
				if definition, derr := g.Control.definitionOf(onboarding); derr == nil {
					if _, oerr := g.Control.ensurePreparedOAuth(r.Context(), onboardingEnvelope(onboarding), onboarding, definition, false); oerr != nil {
						log.Printf("toolhub wizard %s: oauth re-arm: %v", onboardingID, oerr) // #nosec G706 -- onboarding id is store-validated before logging; error text is internal
					} else if fresh, ferr := g.Control.Store.onboarding(onboardingID); ferr == nil && fresh.Phase == api.PhaseAwaitingOAuth {
						onboarding = fresh
						if parsed, perr := url.Parse(onboarding.ProviderAuthorizationURL); perr == nil {
							storedState = parsed.Query().Get("state")
						}
					}
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
			if g.Control.preparedOAuthOnboardingID(storedState) != onboardingID {
				_, _ = io.WriteString(w, wizardWaitPage(lang))
				return
			}
			_, _ = io.WriteString(w, oauthInterstitialPage(onboarding, storedState, lang))
			return
		}
	}
	if onboarding.Phase == api.PhaseAwaitingCreds && r.Method == http.MethodPost {
		g.submitCredentialForm(w, r, onboardingID, "")
		return
	}
	if onboarding.Phase == api.PhaseAwaitingCreds && r.Method == http.MethodGet && onboarding.BrokerAuthorizationURL != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = io.WriteString(w, brokerHopPage(onboarding, lang))
		return
	}
	if onboarding.Phase == api.PhaseAwaitingCreds && r.Method == http.MethodGet {
		cspNonce := api.RandomNonce()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+cspNonce+"'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = io.WriteString(w, g.credentialFormHTML(onboarding, onboardingID, "", nil, cspNonce, lang))
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		switch onboarding.Phase {
		case api.PhasePreparing, api.PhaseReview, api.PhaseConfirmed:
			_, _ = io.WriteString(w, wizardWaitPage(lang))
			return
		case api.PhaseEnabled:
			_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.enabled-title"), locale.T(lang, "page.enabled-body"), lang))
			return
		case api.PhaseFailed:
			_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.failed-title"), locale.T(lang, "page.failed-body"), lang))
			return
		}
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// wizardWaitPage renders the honest in-progress state: the backend is still
// building/reviewing/enabling, so the page meta-refreshes until the phase
// advances to a real step or a terminal state on the same URL.
func wizardWaitPage(lang locale.Lang) string {
	return `<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="refresh" content="6"><title>` + html.EscapeString(locale.T(lang, "page.wait-title")) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0;color:#52606d;line-height:1.6}</style><main aria-live="polite"><h1>` + html.EscapeString(locale.T(lang, "page.wait-h1")) + `</h1><p>` + html.EscapeString(locale.T(lang, "page.wait-body")) + `</p></main></html>`
}

// serveViewer answers the owner-bound browser viewer link: a spent or
// expired nonce gets 410 gone, a forged one 403, a live one opens a short
// polling session and renders the remote-control shell — the nonce never
// reaches any tool result, and the viewer is never an unauthenticated
// published port. /viewer/<id>/frame serves the workload's own screenshot
// frames; /viewer/<id>/input forwards only the allowlisted input calls.
func (g *Gateway) serveViewer(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.Trim(strings.TrimPrefix(r.URL.Path, "/viewer/"), "/"), "/", 2)
	connectionID := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if connectionID == "" || strings.Contains(action, "/") {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "":
		g.serveViewerPage(w, r, connectionID)
	case "frame":
		g.serveViewerFrame(w, r, connectionID)
	case "input":
		g.serveViewerInput(w, r, connectionID)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) serveViewerPage(w http.ResponseWriter, r *http.Request, connectionID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lang := pageLang(r)
	sessionID, err := g.Control.Store.OpenViewerSession(connectionID, linkToken(r), g.Control.now())
	if errors.Is(err, api.ErrLinkGone) {
		goneLinkPage(w, lang)
		return
	}
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	retry, _ := json.Marshal(locale.T(lang, "page.viewer-retry"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = io.WriteString(w, `<!doctype html><html lang="`+html.EscapeString(string(lang))+`"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Browser viewer</title><style>`+
		`:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}`+
		`body{margin:0;min-height:100vh;display:flex;flex-direction:column;align-items:center;padding:16px}main{width:min(1024px,100%)}`+
		`h1{font-size:20px;margin:0 0 8px}p{color:#52606d;line-height:1.5;margin:0 0 12px}`+
		`#bar{display:flex;gap:8px;margin-bottom:12px}#bar input{flex:1;border:1px solid #dfe4ea;border-radius:8px;padding:9px 12px;font:inherit}`+
		`#bar button{border:0;border-radius:8px;padding:9px 16px;background:#175cd3;color:#fff;font:inherit;cursor:pointer}`+
		`#screen{width:100%;background:#17202a;border-radius:12px;cursor:crosshair;display:block}`+
		`#status{color:#98a2b3;font-size:13px;margin-top:8px}</style>`+
		`<main><h1>Browser viewer — `+html.EscapeString(connectionID)+`</h1>`+
		`<p>`+html.EscapeString(locale.T(lang, "page.viewer-hint"))+`</p>`+
		`<div id="bar"><input id="url" placeholder="https://…" autocomplete="off" aria-label="`+html.EscapeString(locale.T(lang, "page.viewer-open"))+`"><button id="go">`+html.EscapeString(locale.T(lang, "page.viewer-open"))+`</button></div>`+
		`<img id="screen" alt="`+html.EscapeString(locale.T(lang, "page.viewer-alt"))+`"><div id="status" aria-live="polite"></div></main>`+
		`<script>`+
		`var S="session=`+html.EscapeString(sessionID)+`";`+
		`var base="/viewer/`+html.EscapeString(connectionID)+`";`+
		`var img=document.getElementById("screen"),st=document.getElementById("status");`+
		`function poll(){var f=new Image();f.onload=function(){img.src=f.src;setTimeout(poll,700)};f.onerror=function(){st.textContent=`+string(retry)+`;setTimeout(poll,3000)};f.src=base+"/frame?"+S+"&n="+Date.now()}`+
		`function send(tool,args){return fetch(base+"/input?"+S,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({tool:tool,arguments:args})})}`+
		`img.addEventListener("click",function(e){var r=img.getBoundingClientRect();var x=Math.round(e.clientX-r.left),y=Math.round(e.clientY-r.top);send("browser_mouse_click_xy",{x:x,y:y})});`+
		`document.addEventListener("keydown",function(e){if(e.target.id==="url")return;var k=e.key;if(k.length===1||k==="Enter"||k==="Backspace"||k==="Tab"||k==="Escape"||k.indexOf("Arrow")===0){e.preventDefault();send("browser_press_key",{key:k})}});`+
		`document.getElementById("go").addEventListener("click",function(){var u=document.getElementById("url").value;if(u)send("browser_navigate",{url:u})});`+
		`poll();`+
		`</script></html>`)
}

// viewerForward runs one allowlisted tool against the session's owner
// binding through the same injector/backend path a projected call uses.
func (g *Gateway) viewerForward(ctx context.Context, sessionID, tool string, arguments map[string]any) (api.BackendResult, error) {
	var out api.BackendResult
	err := g.Control.Store.CallViewerTool(sessionID, tool, g.Control.now(), func(spec api.ToolSpec, effective api.EffectiveBinding) error {
		fields := map[string]string{
			"principal_id": effective.Binding.PrincipalID, "sender_id": "viewer",
			"context_id": effective.Binding.ContextID, "runtime_id": effective.Binding.RuntimeID,
			"policy_version": effective.Binding.PolicyVersion, "binding_id": effective.Binding.ToolBindingID,
			"backend": string(effective.Definition.Transport), "outcome": "admit",
		}
		if effective.Connection != nil {
			fields["connection_id"] = effective.Connection.ConnectionID
		}
		if err := g.audit("admit", fields); err != nil {
			return err
		}
		injection := api.CredentialInjection{Environment: map[string]string{}}
		if g.Injector != nil {
			var injErr error
			injection, injErr = g.Injector(ctx, effective)
			if injErr != nil {
				return injErr
			}
			if injection.Cleanup != nil {
				defer injection.Cleanup()
			}
		}
		effective.CredentialMounts = append([]api.Mount(nil), injection.Mounts...)
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		var result api.BackendResult
		var callErr error
		if caller, ok := g.Backend.(api.EnvBackend); ok {
			result, callErr = caller.CallEnv(callCtx, effective, spec, arguments, injection.Environment)
		} else {
			result, callErr = g.Backend.Call(callCtx, effective, spec, arguments)
		}
		if callErr != nil {
			fields["outcome"] = "deny"
			_ = g.audit("deny", fields)
			return callErr
		}
		out = result
		fields["outcome"] = "allow"
		return g.audit("allow", fields)
	})
	return out, err
}

func viewerSessionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, api.ErrLinkGone):
		http.Error(w, "session gone", http.StatusGone)
	case errors.Is(err, api.ErrUnauthorized):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, api.ErrNotFound), errors.Is(err, api.ErrStale):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "backend failed", http.StatusBadGateway)
	}
}

func (g *Gateway) serveViewerFrame(w http.ResponseWriter, r *http.Request, connectionID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := g.viewerForward(r.Context(), r.URL.Query().Get("session"), "browser_take_screenshot", map[string]any{"type": "jpeg"})
	if err != nil {
		viewerSessionErr(w, err)
		return
	}
	for _, content := range result.Content {
		if image, ok := content.(*mcp.ImageContent); ok && len(image.Data) > 0 {
			mime := image.MIMEType
			if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
				mime = "image/jpeg"
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(image.Data)
			return
		}
	}
	http.Error(w, "no frame", http.StatusBadGateway)
}

func (g *Gateway) serveViewerInput(w http.ResponseWriter, r *http.Request, connectionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil || request.Tool == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	result, err := g.viewerForward(r.Context(), r.URL.Query().Get("session"), request.Tool, request.Arguments)
	if err != nil {
		viewerSessionErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": !result.IsError, "text": result.Text})
}

// serveDevice renders and polls the RFC 8628 device-flow page: the link
// token is the onboarding's form/confirmation nonce, GET starts (or reuses) a
// device authorization and shows the user code + verification URI, POST polls
// once — pending stays a waiting page, success runs finishAuthorization.
func (g *Gateway) serveDevice(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil || g.Control.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	onboardingID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/device/"), "/")
	if onboardingID == "" || strings.Contains(onboardingID, "/") {
		http.NotFound(w, r)
		return
	}
	token := linkToken(r)
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err != nil || !deviceTokenValid(onboarding, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if onboarding.OAuthProvider == "" {
		http.Error(w, "no oauth provider", http.StatusBadRequest)
		return
	}
	lang := pageLang(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		code := onboarding.DeviceCode
		var userCode, verification string
		if code != "" {
			userCode, verification = onboarding.DeviceUserCode, onboarding.DeviceVerification
		} else {
			start, serr := g.Control.OAuth.StartDevice(onboarding.PrincipalID, onboarding.ContextID, onboardingID, onboarding.OAuthProvider)
			if serr != nil {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.device-unavail-title"), api.PublicPrepareError(serr), lang)) // #nosec G705 -- credentialResultPage escapes title and message
				return
			}
			code, userCode, verification = start.DeviceCode, start.UserCode, start.Verification
			onboarding.DeviceCode = code
			onboarding.DeviceUserCode = userCode
			onboarding.DeviceVerification = verification
			onboarding.Revision++
			if err := g.Control.Store.PutOnboarding(onboarding); err != nil {
				http.Error(w, "store failed", http.StatusInternalServerError)
				return
			}
		}
		link := `<a rel="noopener noreferrer" href="` + html.EscapeString(verification) + `">` + html.EscapeString(verification) + `</a>`
		_, _ = io.WriteString(w, `<!doctype html><html lang="`+html.EscapeString(string(lang))+`"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`+html.EscapeString(locale.T(lang, "page.device-title"))+`</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}.code{font:700 32px/1.2 ui-monospace,SFMono-Regular,Consolas,monospace;text-align:center;letter-spacing:.08em;margin:12px 0 22px;color:#17202a}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}a{color:#175cd3}</style><main><h1>`+html.EscapeString(locale.T(lang, "page.device-h1"))+`</h1><p>`+locale.T(lang, "page.device-body", link)+`</p><div class="code">`+html.EscapeString(userCode)+`</div><form method="post"><input type="hidden" name="device_code" value="`+html.EscapeString(code)+`"><input type="hidden" name="t" value="`+html.EscapeString(token)+`"><button type="submit">`+html.EscapeString(locale.T(lang, "page.device-btn"))+`</button></form></main></html>`)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	deviceCode := r.Form.Get("device_code")
	if deviceCode == "" || deviceCode != onboarding.DeviceCode {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	meta, perr := g.Control.OAuth.PollDevice(onboarding.PrincipalID, onboarding.ContextID, onboardingID, onboarding.OAuthProvider, deviceCode)
	if perr != nil {
		// authorization_pending / slow_down are not failures — the user is
		// still typing the code on their other device.
		text := perr.Error()
		if strings.Contains(text, "authorization_pending") || strings.Contains(text, "slow_down") {
			_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.device-pending-title"), locale.T(lang, "page.device-pending-body"), lang))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.login-failed-title"), api.PublicPrepareError(perr), lang)) // #nosec G705 -- credentialResultPage escapes title and message
		return
	}
	if err := g.Control.completeDeviceAuth(r.Context(), onboarding, meta); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.connect-failed-title"), api.PublicPrepareError(err), lang)) // #nosec G705 -- credentialResultPage escapes title and message
		return
	}
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.done-title"), locale.T(lang, "page.done-device-body"), lang))
}

// deviceTokenValid accepts the onboarding's live form or confirmation nonce
// as the /device link token.
func deviceTokenValid(onboarding api.Onboarding, token string) bool {
	if token == "" {
		return false
	}
	if onboarding.FormNonce != "" && token == onboarding.FormNonce && !onboarding.FormExpires.IsZero() {
		return true
	}
	if onboarding.ConfirmationNonce != "" && token == onboarding.ConfirmationNonce && !onboarding.ConfirmationUsed {
		return true
	}
	return false
}

// goneLinkPage answers a spent or dead one-time link: 410 with a hint to
// request a fresh link rather than retry the burnt token.
func goneLinkPage(w http.ResponseWriter, lang locale.Lang) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.gone-title"), locale.T(lang, "page.gone-body"), lang))
}

func credentialResultPage(title, message string, lang locale.Lang) string {
	return `<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0;color:#52606d;line-height:1.6}</style><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p></main></html>`
}

func credentialFormIntro(hints []api.CredentialHint, lang locale.Lang) string {
	if credentialHintsUseAlternatives(hints) {
		return locale.T(lang, "page.form-intro-alt")
	}
	return locale.T(lang, "page.form-intro")
}

func writeCredentialFields(b *strings.Builder, hints, optional []api.CredentialHint, nonce string, recipeFields map[string]api.ConnectionField, prefill map[string]string, lang locale.Lang) {
	if !credentialHintsUseAlternatives(hints) {
		for _, hint := range hints {
			b.WriteString(credentialFieldMarkup(hint, recipeFields[hint.Name], prefill[hint.Name], lang))
		}
		writeOptionalCredentialFields(b, optional, lang)
		return
	}
	groups := map[int][]api.CredentialHint{}
	var plain []api.CredentialHint
	var order []int
	for _, hint := range hints {
		if hint.AlternativeGroup <= 0 {
			plain = append(plain, hint)
			continue
		}
		if _, ok := groups[hint.AlternativeGroup]; !ok {
			order = append(order, hint.AlternativeGroup)
		}
		groups[hint.AlternativeGroup] = append(groups[hint.AlternativeGroup], hint)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return len(groups[order[i]]) < len(groups[order[j]])
	})
	for index, id := range order {
		names := make([]string, 0, len(groups[id]))
		for _, hint := range groups[id] {
			names = append(names, credentialFieldLabel(hint))
		}
		legend := strings.Join(names, ", ")
		if index > 0 {
			b.WriteString(`<details><summary>` + html.EscapeString(locale.T(lang, "page.alt-set", legend)) + `</summary>`)
		}
		group := append(append([]api.CredentialHint{}, plain...), groups[id]...)
		b.WriteString(`<form method="post" enctype="multipart/form-data"><input type="hidden" name="nonce" value="` + html.EscapeString(nonce) + `"><fieldset><legend>`)
		b.WriteString(html.EscapeString(legend))
		b.WriteString("</legend>")
		for _, hint := range group {
			b.WriteString(credentialFieldMarkup(hint, recipeFields[hint.Name], prefill[hint.Name], lang))
		}
		writeOptionalCredentialFields(b, optional, lang)
		b.WriteString(`<button type="submit">` + html.EscapeString(locale.T(lang, "page.submit")) + `</button><button type="submit" formnovalidate>` + html.EscapeString(locale.T(lang, "page.submit-no-probe")) + `</button></fieldset></form>`)
		if index > 0 {
			b.WriteString(`</details>`)
		}
	}
}

// writeOptionalCredentialFields renders discovered non-required inputs so the
// owner can fill them in the same submit without them gating the form.
// writeOptionalCredentialFields renders discovered non-required inputs so the
// owner can fill them in the same submit without them gating the form.
func writeOptionalCredentialFields(b *strings.Builder, optional []api.CredentialHint, lang locale.Lang) {
	if len(optional) == 0 {
		return
	}
	b.WriteString(`<details><summary>` + html.EscapeString(locale.T(lang, "page.opt-group")) + `</summary>`)
	for _, hint := range credentialFormHints(optional) {
		b.WriteString(optionalCredentialFieldMarkup(hint, lang))
	}
	b.WriteString(`</details>`)
}

// credentialFormHTML renders the one-time credential form. errText is the
// public probe error shown on a retry; prefill carries the submitted values
// back page-side — neither ever reaches the model channel.
// credentialFormHTML renders the one-time credential form. errText is the
// public probe error shown on a retry; prefill carries the submitted values
// back page-side — neither ever reaches the model channel.
func (g *Gateway) credentialFormHTML(onboarding api.Onboarding, onboardingID, errText string, prefill map[string]string, cspNonce string, lang locale.Lang) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + html.EscapeString(string(lang)) + `"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(locale.T(lang, "page.connect-title")) + `</title><style>
:root{color-scheme:light;font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}.panel{width:min(640px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{font-size:28px;line-height:1.2;letter-spacing:-.02em;margin:0 0 10px}p{color:#52606d;line-height:1.55;margin:0 0 24px}p.err{color:#b42318;font-weight:600}fieldset{border:1px solid #dfe4ea;border-radius:12px;padding:16px 16px 0;margin:0 0 16px}legend{font-size:16px;font-weight:750;padding:0 6px}label{display:block;font-weight:650;margin:0 0 22px}input,textarea{display:block;width:100%;margin-top:8px;border:1px solid #aab4bf;border-radius:10px;padding:12px 14px;font:inherit;color:#17202a;background:#fff}input[type=file]{padding:10px}textarea{min-height:180px;resize:vertical;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:14px}input:focus,textarea:focus{outline:3px solid rgba(36,99,235,.22);border-color:#2463eb}small{display:block;color:#667583;font-weight:400;line-height:1.45;margin:-12px 0 20px}.tag{display:inline;margin:0 0 0 6px;font-size:12px}.opt{display:flex;align-items:center;gap:8px;margin:-14px 0 18px;font-weight:400;color:#52606d;font-size:14px}.opt input{display:inline;width:auto;margin:0;padding:0}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}button:hover{background:#124aa8}button:focus-visible{outline:3px solid rgba(36,99,235,.3);outline-offset:2px}details{margin:18px 0 22px;color:#52606d}details label{color:#17202a;margin-top:18px}summary{cursor:pointer;font-weight:650}a{color:#175cd3;text-underline-offset:3px}.wait{text-align:center;padding:20px 0}.spin{width:44px;height:44px;margin:0 auto;border:4px solid #dfe4ea;border-top-color:#175cd3;border-radius:50%;animation:sp .9s linear infinite}@keyframes sp{to{transform:rotate(360deg)}}.wait p{margin:0}.wait .lead{font-weight:650;color:#17202a;margin:18px 0 8px}@media(max-width:520px){body{padding:12px}.panel{padding:22px 18px;border-radius:12px}h1{font-size:24px}}
</style><main class="panel"><h1>` + html.EscapeString(locale.T(lang, "page.connect-title")) + `</h1><p>` + html.EscapeString(credentialFormIntro(onboarding.Required, lang)) + `</p>`)
	if errText != "" {
		b.WriteString(`<p class="err">` + html.EscapeString(errText) + `</p>`)
	}
	hints := credentialFormHints(onboarding.Required)
	separate := credentialHintsUseAlternatives(hints)
	if !separate {
		b.WriteString(`<form method="post" enctype="multipart/form-data">`)
		b.WriteString("<input type=\"hidden\" name=\"nonce\" value=\"" + html.EscapeString(onboarding.FormNonce) + "\">")
	}
	var recipeFields map[string]api.ConnectionField
	if recipe := g.Control.recipeOf(onboarding); recipe != nil {
		recipeFields = map[string]api.ConnectionField{}
		for _, field := range recipe.Connection.Fields {
			recipeFields[field.Name] = field
		}
		b.WriteString("<p class=\"meta\">")
		repo := strings.TrimPrefix(recipe.Source.Repository, "https://github.com/")
		if repo != "" {
			b.WriteString("<strong>" + html.EscapeString(repo) + "</strong>")
			if recipe.Source.CommitSHA != "" {
				commit := recipe.Source.CommitSHA
				if len(commit) > 12 {
					commit = commit[:12]
				}
				b.WriteString("@" + html.EscapeString(commit))
			}
		}
		if onboarding.DefinitionVersion != "" {
			b.WriteString(" · " + html.EscapeString(onboarding.DefinitionVersion))
		}
		if recipe.Trust != "" {
			b.WriteString(" · trust " + html.EscapeString(string(recipe.Trust)))
		}
		if len(recipe.Egress) > 0 {
			b.WriteString(" · egress: " + html.EscapeString(strings.Join(recipe.Egress, ", ")))
		}
		b.WriteString("</p>")
	}
	// Tool inventory: read tools are on; write tools are listed as
	// disabled-by-default so the human sees the connector can mutate
	// before saving credentials.
	if definition, err := g.Control.definitionOf(onboarding); err == nil && len(definition.Tools) > 0 {
		var reads, writes []string
		for _, tool := range definition.Tools {
			if tool.Effect == api.WriteEffect {
				writes = append(writes, tool.Name)
			} else {
				reads = append(reads, tool.Name)
			}
		}
		b.WriteString("<p class=\"meta\">" + html.EscapeString(locale.T(lang, "page.tools", strings.Join(reads, ", "))))
		if len(writes) > 0 {
			if len(reads) > 0 {
				b.WriteString("; ")
			}
			b.WriteString(html.EscapeString(locale.T(lang, "page.tools-writes", strings.Join(writes, ", "))))
		}
		b.WriteString("</p>")
	}
	if recipe := g.Control.recipeOf(onboarding); recipe != nil {
		for _, alternative := range recipe.Connection.Alternatives {
			if alternative.URL == "" {
				continue
			}
			b.WriteString("<details><summary>" + html.EscapeString(locale.T(lang, "page.alt-auth")) + "</summary><a rel=\"noopener noreferrer\" href=\"" + html.EscapeString(alternative.URL) + "\">" + html.EscapeString(alternative.Name) + "</a></details>")
		}
	}
	// RFC 8628 escape hatch: when the provider advertises a device
	// authorization endpoint, offer "sign in on another device" — the
	// /device page reuses this link's token.
	if g.Control.OAuth != nil && onboarding.OAuthProvider != "" {
		if prov, ok := g.Control.OAuth.Provider(onboarding.OAuthProvider); ok && prov.DeviceCodeURL != "" {
			b.WriteString("<details><summary>" + html.EscapeString(locale.T(lang, "page.device-alt")) + "</summary><a rel=\"noopener noreferrer\" href=\"/device/" + html.EscapeString(onboardingID) + "?t=" + html.EscapeString(onboarding.FormNonce) + "&amp;lang=" + html.EscapeString(string(lang)) + "\">" + html.EscapeString(locale.T(lang, "page.device-open")) + "</a></details>")
		}
	}
	writeCredentialFields(&b, hints, onboarding.Optional, onboarding.FormNonce, recipeFields, prefill, lang)
	if !separate {
		b.WriteString(`<button type="submit">` + html.EscapeString(locale.T(lang, "page.submit")) + `</button><button type="submit" formnovalidate>` + html.EscapeString(locale.T(lang, "page.submit-no-probe")) + `</button></form>`)
	}
	// The submit blocks on a real server probe; a silent 30+ seconds reads
	// as a refresh loop. Swap the inputs for an honest waiting state while
	// the POST completes — the response page still carries the outcome.
	b.WriteString(`<div class="wait" id="wait" hidden aria-live="polite"><div class="spin"></div><p class="lead">` + html.EscapeString(locale.T(lang, "page.wait-submit")) + `</p><p>` + html.EscapeString(locale.T(lang, "page.wait-usually")) + `<span id="secs">0</span>` + html.EscapeString(locale.T(lang, "page.wait-seconds")) + `</p></div>`)
	b.WriteString(`<script nonce="` + cspNonce + `">var t0=Date.now();document.addEventListener('submit',function(e){var f=e.target;if(f._busy){e.preventDefault();return}f._busy=true;document.querySelectorAll('form,details').forEach(function(el){el.style.display='none'});document.getElementById('wait').hidden=false;var s=document.getElementById('secs');setInterval(function(){s.textContent=Math.floor((Date.now()-t0)/1000)},500)});</script>`)
	b.WriteString("</main></html>")
	return b.String()
}

func credentialSubmitPage(err error, lang locale.Lang) (int, string, string) {
	if errors.Is(err, errCredentialSaved) {
		reason := strings.TrimPrefix(credentialProbeFailure(err, lang), errCredentialSaved.Error()+": ")
		if reason == "" {
			reason = locale.T(lang, "page.saved-no-reason")
		}
		return http.StatusServiceUnavailable, locale.T(lang, "page.saved-title"), locale.T(lang, "page.saved-body", reason)
	}
	if errors.Is(err, api.ErrUnauthorized) && credentialLinkRejected(err) {
		return http.StatusUnauthorized, locale.T(lang, "page.stale-title"), locale.T(lang, "page.stale-body")
	}
	if errors.Is(err, api.ErrInvalid) {
		return http.StatusBadRequest, locale.T(lang, "page.invalid-title"), credentialFormRejection(err, lang)
	}
	reason := credentialProbeFailure(err, lang)
	if reason == "" {
		reason = locale.T(lang, "page.probe-no-reason")
	}
	return http.StatusServiceUnavailable, locale.T(lang, "page.probe-title"), locale.T(lang, "page.probe-body", reason)
}

func credentialLinkRejected(err error) bool {
	text := err.Error()
	return strings.Contains(text, "form nonce") || strings.Contains(text, "expired form") || strings.Contains(text, "credentials not expected")
}

// credentialFieldProvenance renders where the field declaration came from —
// metadata source, ladder rank and an upstream "create a token" link — and
// demotes heuristic-only (unverified) fields to a visible suggestion rather
// than a required input.
// credentialFieldProvenance renders where the field declaration came from —
// metadata source, ladder rank and an upstream "create a token" link — and
// demotes heuristic-only (unverified) fields to a visible suggestion rather
// than a required input.
func credentialFieldProvenance(field api.ConnectionField, lang locale.Lang) string {
	if field.Name == "" {
		return ""
	}
	parts := []string{}
	if field.Source != "" {
		parts = append(parts, locale.T(lang, "page.prov-source", field.Source))
	}
	if field.Rank != "" {
		parts = append(parts, locale.T(lang, "page.prov-rank", string(field.Rank)))
	}
	out := ""
	if len(parts) > 0 {
		out = "<small class=\"prov\">" + html.EscapeString(strings.Join(parts, " · ")) + "</small>"
	}
	if field.Unverified {
		out += "<small class=\"prov\">" + html.EscapeString(locale.T(lang, "page.prov-unverified", field.Source)) + "</small>"
	}
	if field.TokenURL != "" && strings.HasPrefix(field.TokenURL, "https://") {
		out += "<small class=\"prov\"><a rel=\"noopener noreferrer\" href=\"" + html.EscapeString(field.TokenURL) + "\">" + html.EscapeString(locale.T(lang, "page.prov-token")) + "</a></small>"
	}
	return out
}

func credentialFieldMarkup(hint api.CredentialHint, field api.ConnectionField, value string, lang locale.Lang) string {
	prov := credentialFieldProvenance(field, lang)
	if field.Unverified {
		markup := optionalCredentialFieldMarkup(hint, lang)
		return markup + prov
	}
	label := html.EscapeString(credentialFieldLabel(hint))
	inputName := html.EscapeString(hint.Name)
	inputType := "password"
	switch strings.ToLower(hint.Type) {
	case "email":
		inputType = "email"
	case "url":
		inputType = "url"
	case "number", "integer":
		inputType = "number"
	case "string":
		if !hint.Secret {
			inputType = "text"
		}
	}
	if hint.Delivery == "file" {
		return "<label>" + label + " <small class=\"tag\">" + html.EscapeString(locale.T(lang, "page.field-required")) + "</small>" + `<input type="file" name="` + inputName + `" required>` + credentialFieldHint(hint) + prov + "</label>"
	}
	if hint.Delivery == "json" || strings.EqualFold(hint.Type, "json") {
		return "<label>" + label + " <small class=\"tag\">" + html.EscapeString(locale.T(lang, "page.field-required")) + "</small>" + "<textarea name=\"" + inputName + "\" autocomplete=\"off\" required></textarea>" + credentialFieldHint(hint) + prov + "</label>"
	}
	autocomplete := "off"
	switch inputType {
	case "email":
		autocomplete = "email"
	case "password":
		autocomplete = "new-password"
	}
	required := " required"
	if hint.AlternativeGroup > 0 {
		required = ""
	}
	tag := locale.T(lang, "page.field-optional")
	opt := ""
	if required != "" {
		tag = locale.T(lang, "page.field-required")
		opt = `<label class="opt"><input type="checkbox" name="opt:` + inputName + `" value="1"> ` + html.EscapeString(locale.T(lang, "page.opt-out")) + `</label>`
	}
	input := "<input type=\"" + inputType + "\" name=\"" + inputName + "\" autocomplete=\"" + autocomplete + "\"" + required
	if value != "" {
		input += " value=\"" + html.EscapeString(value) + "\""
	}
	input += ">"
	return "<label>" + label + " <small class=\"tag\">" + html.EscapeString(tag) + "</small>" + input + credentialFieldHint(hint) + prov + "</label>" + opt
}

// optionalCredentialFieldMarkup renders a discovered non-required input: same
// control, no required attribute, explicit optional tag.
func optionalCredentialFieldMarkup(hint api.CredentialHint, lang locale.Lang) string {
	label := html.EscapeString(credentialFieldLabel(hint)) + " <small class=\"tag\">" + html.EscapeString(locale.T(lang, "page.field-optional")) + "</small>"
	inputName := html.EscapeString(hint.Name)
	if hint.Delivery == "file" {
		return "<label>" + label + `<input type="file" name="` + inputName + `">` + credentialFieldHint(hint) + "</label>"
	}
	if hint.Delivery == "json" || strings.EqualFold(hint.Type, "json") {
		return "<label>" + label + "<textarea name=\"" + inputName + "\" autocomplete=\"off\"></textarea>" + credentialFieldHint(hint) + "</label>"
	}
	inputType := "text"
	if hint.Secret {
		inputType = "password"
	}
	switch strings.ToLower(hint.Type) {
	case "email":
		inputType = "email"
	case "url":
		inputType = "url"
	case "number", "integer":
		inputType = "number"
	}
	return "<label>" + label + "<input type=\"" + inputType + "\" name=\"" + inputName + "\" autocomplete=\"off\">" + credentialFieldHint(hint) + "</label>"
}

func credentialHintsUseAlternatives(hints []api.CredentialHint) bool {
	for _, hint := range hints {
		if hint.AlternativeGroup > 0 {
			return true
		}
	}
	return false
}

func credentialFormHints(hints []api.CredentialHint) []api.CredentialHint {
	visible := make([]api.CredentialHint, 0, len(hints))
	for _, hint := range hints {
		switch strings.ToUpper(hint.Name) {
		case "HOST", "PORT", "TRANSPORT":
			continue
		}
		visible = append(visible, hint)
	}
	return visible
}

func applyCredentialDefaults(hints []api.CredentialHint, values map[string]string) {
	defaults := map[string]string{"HOST": "127.0.0.1", "PORT": "3000", "TRANSPORT": "stdio"}
	for _, hint := range hints {
		if value := defaults[strings.ToUpper(hint.Name)]; value != "" && values[hint.Name] == "" {
			values[hint.Name] = value
		}
	}
}

func credentialFieldLabel(hint api.CredentialHint) string {
	return strings.ReplaceAll(strings.TrimSpace(hint.Name), "_", " ")
}

func credentialFieldHint(hint api.CredentialHint) string {
	parts := []string{}
	if hint.Alternative != "" {
		parts = append(parts, hint.Alternative)
	}
	if hint.Hint != "" && hint.Hint != "protected loopback form" {
		parts = append(parts, hint.Hint)
	}
	if len(parts) == 0 {
		return ""
	}
	return "<small>" + html.EscapeString(strings.Join(parts, "; ")) + "</small>"
}

func (g *Gateway) serveOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		if r.URL.Query().Get("state") == "" {
			http.Error(w, "missing onboarding", http.StatusBadRequest)
			return
		}
		state := r.URL.Query().Get("state")
		lang := g.oauthLang(r, state)
		flowOnboardingID := g.Control.preparedOAuthOnboardingID(state)
		if err := g.Control.completePreparedOAuth(r.Context(), state, r.URL.Query().Get("code")); err != nil {
			log.Printf("toolhub oauth callback failed: %v", err)
			http.Error(w, "authorization failed; return to chat and retry status", http.StatusForbidden)
			return
		}
		// OAuth done — return the browser to the stable wizard link, which
		// now renders the enablement progress and the final state.
		if done, derr := g.Control.Store.onboarding(flowOnboardingID); derr == nil &&
			done.WizardNonce != "" && g.Control.now().Before(done.WizardExpires) {
			http.Redirect(w, r, g.Control.origin()+"/connect/"+url.PathEscape(done.OnboardingID)+"?k="+url.QueryEscape(done.WizardNonce)+"&lang="+url.QueryEscape(string(lang)), http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = io.WriteString(w, credentialResultPage(locale.T(lang, "page.done-title"), locale.T(lang, "page.done-oauth-body"), lang)) // #nosec G705 -- credentialResultPage escapes title and message
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

// linkToken reads the one-time link token: ?t= is the canonical parameter,
// ?nonce= remains accepted on the same validation path for links already sent.
func linkToken(r *http.Request) string {
	if t := r.URL.Query().Get("t"); t != "" {
		return t
	}
	return r.URL.Query().Get("nonce")
}

// pageLang resolves the page locale: an explicit ?lang= wins, then
// Accept-Language, then the operator fallback.
func pageLang(r *http.Request) locale.Lang {
	if lang, ok := locale.Parse(r.URL.Query().Get("lang")); ok {
		return lang
	}
	if lang, ok := locale.Negotiate(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	return locale.Fallback()
}

// oauthLang resolves like pageLang but lets a language remembered on the
// pending flow win over Accept-Language: the provider's redirect back to
// /oauth/callback carries only state+code, so the user's explicit ?lang=
// choice on the hop link survives through the remembered flow state.
func (g *Gateway) oauthLang(r *http.Request, state string) locale.Lang {
	if lang, ok := locale.Parse(r.URL.Query().Get("lang")); ok {
		return lang
	}
	if lang, ok := g.Control.oauthFlowLang(state); ok {
		return lang
	}
	if lang, ok := locale.Negotiate(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	return locale.Fallback()
}

type publicSurfaceKey struct{}

// withPublicSurface marks a request as arriving on the dedicated public
// listener — the listener itself is the exposure gate.
func withPublicSurface(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicSurfaceKey{}, true)
}

func loopbackHTTP(r *http.Request) bool {
	if r == nil {
		return false
	}
	if marked, _ := r.Context().Value(publicSurfaceKey{}).(bool); marked {
		return true
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
