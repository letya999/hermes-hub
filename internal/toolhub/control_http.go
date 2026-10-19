package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (g *Gateway) addControlTools(server *mcp.Server, auth identity.Envelope, allowed map[string]bool) {
	if g == nil || g.Control == nil || server == nil {
		return
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
		var b strings.Builder
		b.WriteString(`<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Подключение MCP</title><style>
:root{color-scheme:light;font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}.panel{width:min(640px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{font-size:28px;line-height:1.2;letter-spacing:-.02em;margin:0 0 10px}p{color:#52606d;line-height:1.55;margin:0 0 24px}fieldset{border:1px solid #dfe4ea;border-radius:12px;padding:16px 16px 0;margin:0 0 16px}legend{font-size:16px;font-weight:750;padding:0 6px}label{display:block;font-weight:650;margin:0 0 22px}input,textarea{display:block;width:100%;margin-top:8px;border:1px solid #aab4bf;border-radius:10px;padding:12px 14px;font:inherit;color:#17202a;background:#fff}input[type=file]{padding:10px}textarea{min-height:180px;resize:vertical;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:14px}input:focus,textarea:focus{outline:3px solid rgba(36,99,235,.22);border-color:#2463eb}small{display:block;color:#667583;font-weight:400;line-height:1.45;margin:-12px 0 20px}.tag{display:inline;margin:0 0 0 6px;font-size:12px}.opt{display:flex;align-items:center;gap:8px;margin:-14px 0 18px;font-weight:400;color:#52606d;font-size:14px}.opt input{display:inline;width:auto;margin:0;padding:0}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}button:hover{background:#124aa8}button:focus-visible{outline:3px solid rgba(36,99,235,.3);outline-offset:2px}details{margin:18px 0 22px;color:#52606d}details label{color:#17202a;margin-top:18px}summary{cursor:pointer;font-weight:650}a{color:#175cd3;text-underline-offset:3px}.wait{text-align:center;padding:20px 0}.spin{width:44px;height:44px;margin:0 auto;border:4px solid #dfe4ea;border-top-color:#175cd3;border-radius:50%;animation:sp .9s linear infinite}@keyframes sp{to{transform:rotate(360deg)}}.wait p{margin:0}.wait .lead{font-weight:650;color:#17202a;margin:18px 0 8px}@media(max-width:520px){body{padding:12px}.panel{padding:22px 18px;border-radius:12px}h1{font-size:24px}}
</style><main class="panel"><h1>Подключение MCP</h1><p>` + html.EscapeString(credentialFormIntro(onboarding.Required)) + `</p>`)
		hints := credentialFormHints(onboarding.Required)
		separate := credentialHintsUseAlternatives(hints)
		if !separate {
			b.WriteString(`<form method="post" enctype="multipart/form-data">`)
			b.WriteString("<input type=\"hidden\" name=\"nonce\" value=\"" + html.EscapeString(onboarding.FormNonce) + "\">")
		}
		if recipe := g.Control.recipeOf(onboarding); recipe != nil {
			for _, alternative := range recipe.Connection.Alternatives {
				if alternative.URL == "" {
					continue
				}
				b.WriteString("<details><summary>Другой способ авторизации</summary><a rel=\"noopener noreferrer\" href=\"" + html.EscapeString(alternative.URL) + "\">" + html.EscapeString(alternative.Name) + "</a></details>")
			}
		}
		// RFC 8628 escape hatch: when the provider advertises a device
		// authorization endpoint, offer "sign in on another device" — the
		// /device page reuses this link's token.
		if g.Control.OAuth != nil && onboarding.OAuthProvider != "" {
			if prov, ok := g.Control.OAuth.Provider(onboarding.OAuthProvider); ok && prov.DeviceCodeURL != "" {
				b.WriteString("<details><summary>Войти кодом на другом устройстве</summary><a rel=\"noopener noreferrer\" href=\"/device/" + html.EscapeString(onboardingID) + "?t=" + html.EscapeString(onboarding.FormNonce) + "\">Открыть страницу кода устройства</a></details>")
			}
		}
		writeCredentialFields(&b, hints, onboarding.Optional, onboarding.FormNonce)
		if !separate {
			b.WriteString(`<button type="submit">Сохранить и продолжить</button><button type="submit" formnovalidate>Отправить без проверки браузера</button></form>`)
		}
		// The submit blocks on a real server probe; a silent 30+ seconds reads
		// as a refresh loop. Swap the inputs for an honest waiting state while
		// the POST completes — the response page still carries the outcome.
		b.WriteString(`<div class="wait" id="wait" hidden><div class="spin"></div><p class="lead">Проверяю данные и запускаю MCP…</p><p>обычно до минуты — <span id="secs">0</span> с</p></div>`)
		b.WriteString(`<script nonce="` + cspNonce + `">var t0=Date.now();document.addEventListener('submit',function(e){var f=e.target;if(f._busy){e.preventDefault();return}f._busy=true;document.querySelectorAll('form,details').forEach(function(el){el.style.display='none'});document.getElementById('wait').hidden=false;var s=document.getElementById('secs');setInterval(function(){s.textContent=Math.floor((Date.now()-t0)/1000)},500)});</script>`)
		b.WriteString("</main></html>")
		_, _ = io.WriteString(w, b.String())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		file.Close()
		if ferr != nil || len(data) > 64<<10 {
			http.Error(w, "invalid file upload", http.StatusBadRequest)
			return
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			values[hint.Name] = string(data)
		}
	}
	if googleOAuthRequired(onboarding.Required) {
		value, err := googleOAuthFormValue(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		values["GOOGLE_OAUTH_CREDENTIALS"] = value
	}
	applyCredentialDefaults(onboarding.Required, values)
	if err := g.Control.AuthorizeCredentials(r.Context(), onboardingID, nonce, values); err != nil {
		status, title, message := credentialSubmitPage(err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, credentialResultPage(title, message))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, credentialResultPage("Подключение завершено", "Credentials проверены, MCP запущен и включён. Возвращаться в чат и писать «готово» не нужно."))
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
		goneLinkPage(w)
		return
	}
	if err != nil || onboarding.Phase != api.PhaseAwaitingConfirm || onboarding.ConfirmationNonce == "" || nonce != onboarding.ConfirmationNonce || onboarding.ConfirmationUsed || onboarding.ConfirmationExpires.IsZero() || g.Control.now().After(onboarding.ConfirmationExpires) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Подтверждение MCP</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}</style><main><h1>Подтверждение подключения</h1><p>Сервер `+html.EscapeString(onboarding.DefinitionID)+` будет подключён и получит доступ к сохранённым данным.</p><form method="post"><button type="submit">Подтвердить подключение</button></form></main></html>`)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := g.Control.finishAuthorization(r.Context(), onboardingID); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage("Подтверждение не прошло", api.PublicPrepareError(err)))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage("Подключение подтверждено", "Возвращаться в чат и писать «готово» не нужно."))
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
		goneLinkPage(w)
		return
	}
	if err != nil || onboarding.RemoveNonce == "" || nonce != onboarding.RemoveNonce || onboarding.RemoveExpires.IsZero() || g.Control.now().After(onboarding.RemoveExpires) || onboarding.Phase == api.PhaseRemoved {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Удаление MCP</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#b42318;color:#fff;font:inherit;font-weight:700;cursor:pointer}</style><main><h1>Удаление подключения</h1><p>Сервер `+html.EscapeString(onboarding.DefinitionID)+` будет отключён: сохранённые credentials, рабочее пространство и состояние удалятся безвозвратно.</p><form method="post"><button type="submit">Удалить подключение</button></form></main></html>`)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := g.Control.remove(r.Context(), onboardingEnvelope(onboarding), map[string]any{"onboarding_id": onboardingID, "remove_nonce": nonce}); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage("Удаление не прошло", api.PublicPrepareError(err)))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage("Подключение удалено", "Сервер отключён, credentials и рабочее состояние удалены."))
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	onboarding, err := g.Control.Store.onboarding(onboardingID)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if state := r.URL.Query().Get("state"); state != "" && onboarding.ProviderAuthorizationURL != "" {
		if parsed, perr := url.Parse(onboarding.ProviderAuthorizationURL); perr == nil && parsed.Query().Get("state") == state {
			http.Redirect(w, r, onboarding.ProviderAuthorizationURL, http.StatusFound)
			return
		}
	}
	if request := r.URL.Query().Get("request"); request != "" && request == onboarding.BrokerRequestID && onboarding.BrokerAuthorizationURL != "" {
		http.Redirect(w, r, onboarding.BrokerAuthorizationURL, http.StatusFound)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
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
				_, _ = io.WriteString(w, credentialResultPage("Вход по коду недоступен", api.PublicPrepareError(serr)))
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
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Вход по коду</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0 0 22px;color:#52606d;line-height:1.6}.code{font:700 32px/1.2 ui-monospace,SFMono-Regular,Consolas,monospace;text-align:center;letter-spacing:.08em;margin:12px 0 22px;color:#17202a}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}a{color:#175cd3}</style><main><h1>Вход по коду устройства</h1><p>Откройте на любом устройстве <a rel="noopener noreferrer" href="`+html.EscapeString(verification)+`">`+html.EscapeString(verification)+`</a> и введите код:</p><div class="code">`+html.EscapeString(userCode)+`</div><form method="post"><input type="hidden" name="device_code" value="`+html.EscapeString(code)+`"><input type="hidden" name="t" value="`+html.EscapeString(token)+`"><button type="submit">Я ввёл код — проверить</button></form></main></html>`)
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
			_, _ = io.WriteString(w, credentialResultPage("Жду подтверждение", "Код ещё не введён на другом устройстве. Вернитесь и нажмите «Проверить» ещё раз."))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage("Вход не прошёл", api.PublicPrepareError(perr)))
		return
	}
	if err := g.Control.completeDeviceAuth(r.Context(), onboarding, meta); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, credentialResultPage("Подключение не прошло", api.PublicPrepareError(err)))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage("Подключение завершено", "Устройство авторизовано. Возвращаться в чат и писать «готово» не нужно."))
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
func goneLinkPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	_, _ = io.WriteString(w, credentialResultPage("Ссылка уже использована", "Эта одноразовая ссылка больше не действует. Попросите прислать fresh link — модель может вызвать status или remove заново."))
}

func credentialResultPage(title, message string) string {
	return `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0;color:#52606d;line-height:1.6}</style><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p></main></html>`
}

func credentialFormIntro(hints []api.CredentialHint) string {
	if googleOAuthRequired(hints) {
		return "Выберите готовый JSON-файл Google или введите Client ID и Client Secret отдельно. Данные сохранятся в защищённом хранилище и не попадут в чат."
	}
	if credentialHintsUseAlternatives(hints) {
		return "Заполните один набор. Открыт самый короткий. Остальные способы свёрнуты — их поля отправлять не нужно."
	}
	return "Введите запрошенные поля. Они сохранятся в защищённом хранилище и не попадут в чат."
}

func writeCredentialFields(b *strings.Builder, hints, optional []api.CredentialHint, nonce string) {
	if !credentialHintsUseAlternatives(hints) {
		for _, hint := range hints {
			b.WriteString(credentialFieldMarkup(hint))
		}
		writeOptionalCredentialFields(b, optional)
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
			b.WriteString(`<details><summary>Другой набор: ` + html.EscapeString(legend) + `</summary>`)
		}
		group := append(append([]api.CredentialHint{}, plain...), groups[id]...)
		b.WriteString(`<form method="post" enctype="multipart/form-data"><input type="hidden" name="nonce" value="` + html.EscapeString(nonce) + `"><fieldset><legend>`)
		b.WriteString(html.EscapeString(legend))
		b.WriteString("</legend>")
		for _, hint := range group {
			b.WriteString(credentialFieldMarkup(hint))
		}
		writeOptionalCredentialFields(b, optional)
		b.WriteString(`<button type="submit">Сохранить и продолжить</button><button type="submit" formnovalidate>Отправить без проверки браузера</button></fieldset></form>`)
		if index > 0 {
			b.WriteString(`</details>`)
		}
	}
}

// writeOptionalCredentialFields renders discovered non-required inputs so the
// owner can fill them in the same submit without them gating the form.
func writeOptionalCredentialFields(b *strings.Builder, optional []api.CredentialHint) {
	if len(optional) == 0 {
		return
	}
	b.WriteString(`<details><summary>Необязательные параметры</summary>`)
	for _, hint := range credentialFormHints(optional) {
		b.WriteString(optionalCredentialFieldMarkup(hint))
	}
	b.WriteString(`</details>`)
}

func credentialSubmitPage(err error) (int, string, string) {
	if errors.Is(err, errCredentialSaved) {
		reason := strings.TrimPrefix(credentialProbeFailure(err), errCredentialSaved.Error()+": ")
		if reason == "" {
			reason = "сервер не завершил привязку."
		}
		return http.StatusServiceUnavailable, "Подключение не завершено", "Данные сохранены. Завершить привязку из формы не удалось: " + reason + " Повторите подтверждение в чате."
	}
	if errors.Is(err, api.ErrUnauthorized) && credentialLinkRejected(err) {
		return http.StatusUnauthorized, "Ссылка устарела", "Форма больше не действует. Данные не сохранены. Попросите в чате новую ссылку."
	}
	if errors.Is(err, api.ErrInvalid) {
		return http.StatusBadRequest, "Форма заполнена не так", credentialFormRejection(err)
	}
	reason := strings.TrimPrefix(credentialProbeFailure(err), "Проверка не прошла. ")
	if reason == "" {
		reason = "Сервер не подтвердил вход."
	}
	return http.StatusServiceUnavailable, "Проверка не прошла", "Токен не сохранён. " + reason
}

func credentialLinkRejected(err error) bool {
	text := err.Error()
	return strings.Contains(text, "form nonce") || strings.Contains(text, "expired form") || strings.Contains(text, "credentials not expected")
}

func credentialFieldMarkup(hint api.CredentialHint) string {
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
	if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
		return `<fieldset><legend>Данные OAuth-клиента Google</legend><label>Выберите скачанный JSON-файл<input type="file" name="google_oauth_file" accept=".json,application/json"></label><small>Подойдёт файл с именем вроде client_secret_….apps.googleusercontent.com.json.</small><details><summary>Или ввести значения вручную</summary><label>Client ID<input type="text" name="google_oauth_client_id" autocomplete="off"></label><label>Client Secret<input type="password" name="google_oauth_client_secret" autocomplete="off"></label></details></fieldset>`
	}
	if hint.Delivery == "file" {
		return "<label>" + label + " <small class=\"tag\">обязательное</small>" + `<input type="file" name="` + inputName + `" required>` + credentialFieldHint(hint) + "</label>"
	}
	if hint.Delivery == "json" || strings.EqualFold(hint.Type, "json") {
		return "<label>" + label + " <small class=\"tag\">обязательное</small>" + "<textarea name=\"" + inputName + "\" autocomplete=\"off\" required></textarea>" + credentialFieldHint(hint) + "</label>"
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
	tag := "необязательное"
	opt := ""
	if required != "" {
		tag = "обязательное"
		opt = `<label class="opt"><input type="checkbox" name="opt:` + inputName + `" value="1"> Поле не требуется — запустить без него</label>`
	}
	return "<label>" + label + " <small class=\"tag\">" + tag + "</small>" + "<input type=\"" + inputType + "\" name=\"" + inputName + "\" autocomplete=\"" + autocomplete + "\"" + required + ">" + credentialFieldHint(hint) + "</label>" + opt
}

// optionalCredentialFieldMarkup renders a discovered non-required input: same
// control, no required attribute, explicit "необязательное" tag.
func optionalCredentialFieldMarkup(hint api.CredentialHint) string {
	label := html.EscapeString(credentialFieldLabel(hint)) + " <small class=\"tag\">необязательное</small>"
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

func googleOAuthRequired(hints []api.CredentialHint) bool {
	for _, hint := range hints {
		if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
			return true
		}
	}
	return false
}

func googleOAuthFormValue(r *http.Request) (string, error) {
	var data []byte
	file, _, err := r.FormFile("google_oauth_file")
	if err == nil {
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, (64<<10)+1))
		if err != nil || len(data) > 64<<10 {
			return "", fmt.Errorf("не удалось прочитать JSON-файл")
		}
	} else if err != http.ErrMissingFile {
		return "", fmt.Errorf("не удалось загрузить JSON-файл")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		clientID := strings.TrimSpace(r.FormValue("google_oauth_client_id"))
		clientSecret := strings.TrimSpace(r.FormValue("google_oauth_client_secret"))
		if clientID == "" || clientSecret == "" {
			return "", fmt.Errorf("выберите JSON-файл или заполните Client ID и Client Secret")
		}
		data, _ = json.Marshal(map[string]any{"installed": map[string]any{"client_id": clientID, "client_secret": clientSecret, "redirect_uris": []string{"http://localhost:3000/oauth2callback"}}})
	}
	var document map[string]map[string]any
	if json.Unmarshal(data, &document) != nil {
		return "", fmt.Errorf("выбранный файл не является корректным JSON")
	}
	credentials := document["installed"]
	if credentials == nil {
		credentials = document["web"]
	}
	if id, _ := credentials["client_id"].(string); strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("в JSON-файле отсутствует client_id")
	}
	if secret, _ := credentials["client_secret"].(string); strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("в JSON-файле отсутствует client_secret")
	}
	return string(data), nil
}

func credentialFieldLabel(hint api.CredentialHint) string {
	if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
		return "Файл OAuth-клиента Google (JSON)"
	}
	return strings.ReplaceAll(strings.TrimSpace(hint.Name), "_", " ")
}

func credentialFieldHint(hint api.CredentialHint) string {
	if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
		return "<small>Вставьте содержимое файла gcp-oauth.keys.json, скачанного для OAuth client ID типа Desktop app. Email, host, port и transport здесь не нужны.</small>"
	}
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
		if err := g.Control.completePreparedOAuth(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code")); err != nil {
			http.Error(w, "authorization failed; return to chat and retry status", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = io.WriteString(w, credentialResultPage("Подключение завершено", "Можно вернуться в чат. Доступ к календарю проверен."))
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
