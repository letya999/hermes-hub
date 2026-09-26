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
			if name == "required_credentials" || (name == "confirm" || name == "status") && body["phase"] == PhaseAwaitingOAuth {
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
	case "discover":
		properties = map[string]any{"query": map[string]any{"type": "string", "maxLength": 256}}
		description += " Read-only search: returns a prepared repository and up to four matching registry alternatives. Select its candidate_id with prepare_source; discovery does not install."
	case "prepare_source":
		properties["source"] = map[string]any{"type": "string"}
		properties["candidate_id"] = map[string]any{"type": "string"}
		properties["request_key"] = map[string]any{"type": "string"}
		description += " For every explicit install/add request containing a GitHub repository URL, call this first with that URL in source, even if chat history mentions an older installation. Do not call remove, revoke or status first. Review and build may outlive the call: on phase=preparing poll status with the returned onboarding_id."
	case "rotate", "disable", "revoke", "remove":
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
	if formURL == "" {
		formURL, _ = body["authorization_url"].(string)
	}
	if formURL == "" || request == nil {
		return nil
	}
	if request.Params != nil && len(request.Params.InputResponses) > 0 {
		return nil
	}
	message := "Open the protected credential form."
	if _, ok := body["authorization_url"]; ok {
		message = "Open the protected credential or OAuth URL."
	}
	if !clientSupportsURLElicitation(request) {
		encoded, _ := json.Marshal(body)
		return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: message + " " + formURL + "\n" + string(encoded)}}}
	}
	params := &mcp.ElicitParams{Mode: "url", Message: message, URL: formURL}
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		var b strings.Builder
		b.WriteString(`<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Подключение MCP</title><style>
:root{color-scheme:light;font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}.panel{width:min(640px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{font-size:28px;line-height:1.2;letter-spacing:-.02em;margin:0 0 10px}p{color:#52606d;line-height:1.55;margin:0 0 24px}fieldset{border:0;padding:0;margin:0}legend{font-size:18px;font-weight:750;margin-bottom:18px}label{display:block;font-weight:650;margin:0 0 22px}input,textarea{display:block;width:100%;margin-top:8px;border:1px solid #aab4bf;border-radius:10px;padding:12px 14px;font:inherit;color:#17202a;background:#fff}input[type=file]{padding:10px}textarea{min-height:180px;resize:vertical;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:14px}input:focus,textarea:focus{outline:3px solid rgba(36,99,235,.22);border-color:#2463eb}small{display:block;color:#667583;font-weight:400;line-height:1.45;margin:-12px 0 20px}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}button:hover{background:#124aa8}button:focus-visible{outline:3px solid rgba(36,99,235,.3);outline-offset:2px}details{margin:18px 0 22px;color:#52606d}details label{color:#17202a;margin-top:18px}summary{cursor:pointer;font-weight:650}a{color:#175cd3;text-underline-offset:3px}@media(max-width:520px){body{padding:12px}.panel{padding:22px 18px;border-radius:12px}h1{font-size:24px}}
</style><main class="panel"><h1>Подключение MCP</h1><p>Выберите готовый JSON-файл Google или введите Client ID и Client Secret отдельно. Данные сохранятся в защищённом хранилище и не попадут в чат.</p><form method="post" enctype="multipart/form-data">`)
		b.WriteString("<input type=\"hidden\" name=\"nonce\" value=\"" + html.EscapeString(onboarding.FormNonce) + "\">")
		if onboarding.Recipe != nil {
			for _, alternative := range onboarding.Recipe.Connection.Alternatives {
				if alternative.URL == "" {
					continue
				}
				b.WriteString("<details><summary>Другой способ авторизации</summary><a rel=\"noopener noreferrer\" href=\"" + html.EscapeString(alternative.URL) + "\">" + html.EscapeString(alternative.Name) + "</a></details>")
			}
		}
		for _, hint := range credentialFormHints(onboarding.Required) {
			b.WriteString(credentialFieldMarkup(hint))
		}
		b.WriteString("<button type=\"submit\">Сохранить и продолжить</button></form></main></html>")
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, credentialResultPage("Подключение не завершено", "Данные сохранены, но проверка запуска не прошла. ToolHub сохранит состояние; повторно вводить credentials не нужно. Вернитесь в чат — агент увидит точную причину и сможет повторить запуск."))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, credentialResultPage("Подключение завершено", "Credentials проверены, MCP запущен и включён. Возвращаться в чат и писать «готово» не нужно."))
}

func credentialResultPage(title, message string) string {
	return `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title><style>:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:28px;letter-spacing:-.02em}p{margin:0;color:#52606d;line-height:1.6}</style><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p></main></html>`
}

func credentialFieldMarkup(hint CredentialHint) string {
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
	if hint.Delivery == "json" || strings.EqualFold(hint.Type, "json") {
		return "<label>" + label + "<textarea name=\"" + inputName + "\" autocomplete=\"off\" required></textarea>" + credentialFieldHint(hint) + "</label>"
	}
	autocomplete := "off"
	if inputType == "email" {
		autocomplete = "email"
	}
	return "<label>" + label + "<input type=\"" + inputType + "\" name=\"" + inputName + "\" autocomplete=\"" + autocomplete + "\" required>" + credentialFieldHint(hint) + "</label>"
}

func credentialFormHints(hints []CredentialHint) []CredentialHint {
	visible := make([]CredentialHint, 0, len(hints))
	for _, hint := range hints {
		switch strings.ToUpper(hint.Name) {
		case "HOST", "PORT", "TRANSPORT":
			continue
		}
		visible = append(visible, hint)
	}
	return visible
}

func applyCredentialDefaults(hints []CredentialHint, values map[string]string) {
	defaults := map[string]string{"HOST": "127.0.0.1", "PORT": "3000", "TRANSPORT": "stdio"}
	for _, hint := range hints {
		if value := defaults[strings.ToUpper(hint.Name)]; value != "" && values[hint.Name] == "" {
			values[hint.Name] = value
		}
	}
}

func googleOAuthRequired(hints []CredentialHint) bool {
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

func credentialFieldLabel(hint CredentialHint) string {
	if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
		return "Файл OAuth-клиента Google (JSON)"
	}
	return strings.ReplaceAll(strings.TrimSpace(hint.Name), "_", " ")
}

func credentialFieldHint(hint CredentialHint) string {
	if hint.Name == "GOOGLE_OAUTH_CREDENTIALS" {
		return "<small>Вставьте содержимое файла gcp-oauth.keys.json, скачанного для OAuth client ID типа Desktop app. Email, host, port и transport здесь не нужны.</small>"
	}
	parts := []string{}
	if hint.Delivery != "" {
		parts = append(parts, "delivery: "+hint.Delivery)
	}
	if hint.Target != "" && hint.Target != hint.Name {
		parts = append(parts, "target: "+hint.Target)
	}
	if hint.Alternative != "" {
		parts = append(parts, "alternative: "+hint.Alternative)
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
