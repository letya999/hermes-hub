package communication

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
)

const credentialFormTTL = 10 * time.Minute

type credentialForm struct {
	OwnerID   string
	Envelope  identity.Envelope
	Keys      []string
	NonceHash string
	ExpiresAt time.Time
	Used      bool
}

type credentialFormRequest struct {
	Service string   `json:"service,omitempty"`
	Keys    []string `json:"keys,omitempty"`
}

type credentialFormResponse struct {
	Input   string   `json:"input"`
	FormURL string   `json:"form_url"`
	Fields  []string `json:"fields"`
}

func (g *Gateway) handleCredentialFormRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request credentialFormRequest
	if decodeJSON(r, &request) != nil {
		http.Error(w, "invalid form request", http.StatusBadRequest)
		return
	}
	keys, err := g.formKeys(g.user(caller.PrincipalID), request.Service, request.Keys)
	if err != nil {
		http.Error(w, "credential form rejected", http.StatusBadRequest)
		return
	}
	response, err := g.createCredentialForm(caller, keys)
	if err != nil {
		http.Error(w, "credential form unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, response)
}

func (g *Gateway) formKeys(user User, service string, requested []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, info := range stack.ServiceCatalog() {
		if info.SelfService {
			for _, key := range info.Requires {
				allowed[key] = true
			}
		}
	}
	for key := range user.ConfiguredEnv {
		allowed[key] = true
	}
	if len(requested) == 0 && service != "" {
		info, ok := stack.ServiceInfoByName(service)
		if !ok || !info.SelfService {
			return nil, errors.New("unknown self-service connector")
		}
		requested = info.Requires
	}
	if len(requested) == 0 {
		return nil, errors.New("credential fields are required")
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(requested))
	for _, key := range requested {
		key = strings.TrimSpace(key)
		if !allowed[key] || stack.GatewayOwnedSecret(key) || seen[key] {
			if !allowed[key] || stack.GatewayOwnedSecret(key) {
				return nil, errors.New("credential field is not allowed")
			}
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

func (g *Gateway) createCredentialForm(auth identity.Envelope, keys []string) (credentialFormResponse, error) {
	origin, err := g.formOrigin()
	if err != nil {
		return credentialFormResponse{}, err
	}
	nonce, err := randomFormToken()
	if err != nil {
		return credentialFormResponse{}, err
	}
	id, err := randomFormToken()
	if err != nil {
		return credentialFormResponse{}, err
	}
	hash := sha256.Sum256([]byte(nonce))
	g.formsMu.Lock()
	if g.forms == nil {
		g.forms = map[string]credentialForm{}
	}
	for formID, form := range g.forms {
		if form.OwnerID == auth.PrincipalID && g.now().After(form.ExpiresAt) {
			delete(g.forms, formID)
		}
	}
	g.forms[id] = credentialForm{OwnerID: auth.PrincipalID, Envelope: auth, Keys: append([]string(nil), keys...), NonceHash: hex.EncodeToString(hash[:]), ExpiresAt: g.now().Add(credentialFormTTL)}
	g.formsMu.Unlock()
	return credentialFormResponse{Input: "protected-form", FormURL: origin + "/credentials/" + id + "?nonce=" + url.QueryEscape(nonce), Fields: append([]string(nil), keys...)}, nil
}

func (g *Gateway) formOrigin() (string, error) {
	origin := strings.TrimRight(strings.TrimSpace(g.config.FormOrigin), "/")
	if origin == "" {
		listen := strings.TrimRight(strings.TrimSpace(g.config.ListenAddr), "/")
		if listen == "" {
			return "", errors.New("communication form listener is not configured")
		}
		if strings.Contains(listen, "://") {
			origin = listen
		} else if host, port, err := net.SplitHostPort(listen); err == nil && (host == "" || host == "0.0.0.0" || host == "::" || host == "[::]") {
			origin = "http://127.0.0.1:" + port
		} else {
			origin = "http://" + listen
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid communication form origin")
	}
	return origin, nil
}

func randomFormToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (g *Gateway) credentialForm(id, nonce string, claim bool) (credentialForm, error) {
	g.formsMu.Lock()
	defer g.formsMu.Unlock()
	form, ok := g.forms[id]
	if !ok || form.ExpiresAt.Before(g.now()) || form.Used {
		return credentialForm{}, errors.New("form unavailable")
	}
	hash := sha256.Sum256([]byte(nonce))
	if subtle.ConstantTimeCompare([]byte(form.NonceHash), []byte(hex.EncodeToString(hash[:]))) != 1 {
		return credentialForm{}, errors.New("form unavailable")
	}
	if claim {
		form.Used = true
		g.forms[id] = form
	}
	return form, nil
}

func (g *Gateway) serveCredentialForm(w http.ResponseWriter, r *http.Request) {
	if !communicationLoopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/credentials/"), "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if r.Method == http.MethodGet {
		form, err := g.credentialForm(id, nonce, false)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var body strings.Builder
		body.WriteString("<!doctype html><meta name=\"referrer\" content=\"no-referrer\"><title>Hermes credentials</title><h1>Protected connector setup</h1><form method=post>")
		body.WriteString("<input type=hidden name=nonce value=\"" + html.EscapeString(nonce) + "\">")
		for _, key := range form.Keys {
			body.WriteString("<label>" + html.EscapeString(key) + "<input type=password name=\"" + html.EscapeString(key) + "\" autocomplete=off required></label><br>")
		}
		body.WriteString("<button type=submit>Save securely</button></form>")
		if slices.Contains(form.Keys, "GOOGLE_OAUTH_CLIENT_ID") || slices.Contains(form.Keys, "GOOGLE_OAUTH_CLIENT_SECRET") {
			body.WriteString("<p>После сохранения Hermes продолжит подключение Google и покажет официальный OAuth URL. Client secret не отправляйте в чат.</p>")
		}
		_, _ = io.WriteString(w, body.String())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	form, err := g.credentialForm(id, nonce, false)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	values := make(map[string]string, len(form.Keys))
	for _, key := range form.Keys {
		value := r.PostForm.Get(key)
		if strings.TrimSpace(value) == "" {
			http.Error(w, "all fields are required", http.StatusBadRequest)
			return
		}
		values[key] = value
	}
	if _, err := g.credentialForm(id, nonce, true); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := g.persistCredentialForm(r, form, values); err != nil {
		http.Error(w, "credential update rejected", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><title>Saved</title><p>Credentials saved securely. Return to Hermes and continue the connection.</p>")
}

func (g *Gateway) persistCredentialForm(r *http.Request, form credentialForm, values map[string]string) error {
	if g.config.RuntimeURL != "" {
		request := hubruntime.SelfEnvRequest{Envelope: form.Envelope, OrganizationID: g.config.OrganizationID, UserID: form.OwnerID, ActorID: form.OwnerID, ScopeID: "user:" + form.OwnerID, Values: values}
		body, err := json.Marshal(request)
		if err != nil {
			return err
		}
		defer clear(body)
		requestURL := strings.TrimRight(g.config.RuntimeURL, "/") + "/v1/self-env"
		httpRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpRequest.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
		httpRequest.Header.Set("Content-Type", "application/json")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpRequest)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			return errors.New("runtime rejected credential update")
		}
		return nil
	}
	user := g.user(form.OwnerID)
	if g.secrets != nil {
		infos, err := g.secrets.Set(form.OwnerID, values)
		if err != nil {
			return err
		}
		for _, info := range infos {
			if err := g.secrets.SetTerminalExposure(form.OwnerID, info.Name, true); err != nil {
				return err
			}
		}
	} else {
		allowed := make([]string, 0, len(user.ConfiguredEnv))
		for key := range user.ConfiguredEnv {
			allowed = append(allowed, key)
		}
		if _, err := envstore.UpdateValues(filepath.Join(user.StateDir, envstore.FileName), values, strings.Join(allowed, ","), strings.Join(stack.GatewaySecretKeys(), ",")); err != nil {
			return err
		}
	}
	if g.restart != nil {
		request := hubruntime.ExecuteRequest{}
		if g.config.Supervised {
			request = supervisorRestartRequest(form.Envelope, g.config.OrganizationID, user.ID, user.ID, "user:"+user.ID, "credentials-"+form.OwnerID)
		}
		return g.restart(r.Context(), request)
	}
	return nil
}

func communicationLoopbackHTTP(r *http.Request) bool {
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
