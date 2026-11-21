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

// Route switching is gated by the PDP right models.switch — a passport-shaped
// registry carries no per-owner switch map, so the PDP answer is the only
// decision. Denied owners get 403; granted owners pick any declared route.
func TestRouteSwitchRequiresGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `runtime_model: hermes
providers:
  a: {url: ` + upstream.URL + `}
models:
  everyday:
    ladder:
      - {provider: a, model: m1}
  reasoning:
    ladder:
      - {provider: a, model: m2}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	decider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"allow": req.Operation != "switch"})
	}))
	defer decider.Close()
	t.Setenv("HUB_TOOLHUB_URL", decider.URL)
	t.Setenv("HUB_TOOLHUB_CONTROL", "tok")
	handler, err := ModelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "admintoken")
	if err != nil {
		t.Fatal(err)
	}
	put := func(model string) int {
		req := httptest.NewRequest(http.MethodPut, "/v1/model", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Authorization", "Bearer admintoken")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := put("reasoning"); code != http.StatusForbidden {
		t.Fatalf("models.switch denied but PUT returned %d", code)
	}
}
