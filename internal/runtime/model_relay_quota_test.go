package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registryQuotaHandler builds a registry relay bound to a fake cliproxy
// management API; the space secrets land in env exactly like the rendered
// model-relay.<env>.env file.
func registryQuotaHandler(t *testing.T, mgmt http.HandlerFunc, key string) http.Handler {
	t.Helper()
	mgmtServer := httptest.NewServer(mgmt)
	t.Cleanup(mgmtServer.Close)
	t.Setenv("HUB_CLIPROXY_MGMT_URL", mgmtServer.URL)
	t.Setenv("HUB_CLIPROXY_MGMT_KEY", key)
	t.Setenv("HUB_CLIPROXY_AUTH_INDEX", "")
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `providers:
  p:
    url: ` + mgmtServer.URL + `
models:
  default:
    ladder:
      - {provider: p, model: m1}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "admintoken")
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func quotaRequest(t *testing.T, handler http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestModelRelayQuotaRendersSubscriptionLines(t *testing.T) {
	var apiCallBody map[string]any
	handler := registryQuotaHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"provider": "antigravity", "auth_index": "idx-1", "project_id": "proj", "email": "u@example.com"},
			}})
		case "/v0/management/api-call":
			_ = json.NewDecoder(r.Body).Decode(&apiCallBody)
			body := `{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","resetTime":"2026-10-07T07:22:39Z","remainingFraction":0.924}]}]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": body})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, "mgmt-test")
	req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
	req.Header.Set("Authorization", "Bearer admintoken")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("quota rejected: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("quota payload malformed: %v %s", err, response.Body.String())
	}
	joined := strings.Join(payload.Lines, "\n")
	for _, want := range []string{"u@example.com", "Gemini Models", "92.4%", "07.10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("quota line missing %q: %s", want, joined)
		}
	}
	header, _ := apiCallBody["header"].(map[string]any)
	if header["Authorization"] != "Bearer $TOKEN$" || header["User-Agent"] != "antigravity" {
		t.Fatalf("api-call headers: %v", header)
	}
}

func TestModelRelayQuotaRequiresAdminToken(t *testing.T) {
	reached := 0
	handler := registryQuotaHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusInternalServerError)
	}, "mgmt-test")
	for _, token := range []string{"", "wrong"} {
		if response := quotaRequest(t, handler, token); response.Code != http.StatusForbidden {
			t.Fatalf("quota served without admin token %q: %d", token, response.Code)
		}
	}
	if reached != 0 {
		t.Fatal("management API reached without admin bearer")
	}
}

func TestModelRelayQuotaHonestWithoutKey(t *testing.T) {
	reached := 0
	handler := registryQuotaHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		reached++
	}, "")
	response := quotaRequest(t, handler, "admintoken")
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Lines) != 1 || !strings.Contains(payload.Lines[0], "не настроен") {
		t.Fatalf("unconfigured quota status: %v", payload.Lines)
	}
	if reached != 0 {
		t.Fatal("management API reached without management key")
	}
}

func TestModelRelayQuotaFallsBackToRelayCliproxyURL(t *testing.T) {
	reached := 0
	mgmt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
	}))
	defer mgmt.Close()
	handler := registryQuotaHandler(t, func(w http.ResponseWriter, _ *http.Request) {}, "mgmt-test")
	t.Setenv("HUB_CLIPROXY_MGMT_URL", "")
	t.Setenv("HUB_MODEL_RELAY_CLIPROXY", mgmt.URL)
	response := quotaRequest(t, handler, "admintoken")
	var payload struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if reached != 1 || len(payload.Lines) != 1 || !strings.Contains(payload.Lines[0], "нет активного OAuth-аккаунта") {
		t.Fatalf("cliproxy fallback missed: reached=%d lines=%v", reached, payload.Lines)
	}
}

func TestModelRelayQuotaSelectsOnlyActiveAntigravityCredential(t *testing.T) {
	files := []cliproxyAuthFile{
		{Provider: "codex", AuthIndex: "other"},
		{Provider: "antigravity", AuthIndex: "disabled", Disabled: true},
		{Provider: "antigravity", AuthIndex: "own"},
	}
	if selected, ok := pickAuthFile(files, ""); !ok || selected.AuthIndex != "own" {
		t.Fatalf("unexpected default quota credential: %v %v", selected.Provider, ok)
	}
	for _, index := range []string{"other", "disabled", "missing"} {
		if _, ok := pickAuthFile(files, index); ok {
			t.Fatalf("accepted unrelated or disabled credential %q", index)
		}
	}
}
