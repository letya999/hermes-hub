package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
)

// Fixture is a local authorization server used by tests. It is not a live provider.
type Fixture struct {
	Server  *httptest.Server
	mu      sync.Mutex
	codes   map[string]string
	devices map[string]bool
	refresh map[string]int
}

func NewFixture() *Fixture {
	f := &Fixture{codes: map[string]string{}, devices: map[string]bool{}, refresh: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/device/code", f.device)
	mux.HandleFunc(ProtectedResourceWellKnown, f.resource)
	mux.HandleFunc(AuthorizationServerWellKnown, f.asMeta)
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *Fixture) Close() { f.Server.Close() }

func (f *Fixture) Provider() Provider {
	return Provider{
		Name:          "fixture",
		AuthorizeURL:  f.Server.URL + "/authorize",
		TokenURL:      f.Server.URL + "/token",
		DeviceCodeURL: f.Server.URL + "/device/code",
	}
}

func (f *Fixture) authorize(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("code_challenge")
	state := r.URL.Query().Get("state")
	if challenge == "" || r.URL.Query().Get("code_challenge_method") != "S256" {
		http.Error(w, "pkce required", http.StatusBadRequest)
		return
	}
	code := "code-" + state
	f.mu.Lock()
	f.codes[code] = challenge
	f.mu.Unlock()
	http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code="+code+"&state="+state, http.StatusFound)
}

func (f *Fixture) token(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	parsed, _ := url.ParseQuery(string(body))
	form := map[string]string{}
	for key, values := range parsed {
		if len(values) > 0 {
			form[key] = values[0]
		}
	}
	grant := form["grant_type"]
	switch grant {
	case "authorization_code":
		f.mu.Lock()
		challenge, ok := f.codes[form["code"]]
		f.mu.Unlock()
		if !ok || pkceChallenge(form["code_verifier"]) != challenge {
			http.Error(w, "invalid_grant", http.StatusBadRequest)
			return
		}
		writeTokens(w, "access-"+form["code"], "refresh-"+form["code"])
	case "urn:ietf:params:oauth:grant-type:device_code":
		f.mu.Lock()
		ok := f.devices[form["device_code"]]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "authorization_pending", http.StatusBadRequest)
			return
		}
		writeTokens(w, "access-"+form["device_code"], "refresh-"+form["device_code"])
	case "refresh_token":
		f.mu.Lock()
		f.refresh[form["refresh_token"]]++
		n := f.refresh[form["refresh_token"]]
		f.mu.Unlock()
		writeTokens(w, "access-rotated-"+form["refresh_token"], "refresh-rotated-"+form["refresh_token"]+"-"+itoa(n))
	default:
		http.Error(w, "unsupported_grant", http.StatusBadRequest)
	}
}

func (f *Fixture) device(w http.ResponseWriter, r *http.Request) {
	code := "dev-code"
	f.mu.Lock()
	f.devices[code] = true
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"device_code":      code,
		"user_code":        "ABCD-1234",
		"verification_uri": f.Server.URL + "/device",
	})
}

func (f *Fixture) resource(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []string{f.Server.URL}})
}

func (f *Fixture) asMeta(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"authorization_endpoint":        f.Server.URL + "/authorize",
		"token_endpoint":                f.Server.URL + "/token",
		"device_authorization_endpoint": f.Server.URL + "/device/code",
	})
}

func (f *Fixture) RefreshCount(token string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refresh[token]
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [12]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
