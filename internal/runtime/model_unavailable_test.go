package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service that does not serve any of its declared models is model-level
// exhausted: the relay walks the hop's models, then advances the ladder to
// the next service instead of returning the first service's failure.
func TestModelUnavailableTriesNextModelThenNextService(t *testing.T) {
	var sawA []string
	svcA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sawA = append(sawA, string(b))
		http.Error(w, `{"error":{"code":"model_not_found"}}`, http.StatusNotFound)
	}))
	defer svcA.Close()
	svcB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer svcB.Close()
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := `runtime_model: hermes
providers:
  a: {allow_private: true, url: ` + svcA.URL + `}
  b: {allow_private: true, url: ` + svcB.URL + `}
models:
  everyday:
    ladder:
      - {provider: a, models: [m1, m2]}
      - {provider: b, model: m3}
`
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := ModelRelayRegistryHandler(registry, filepath.Join(dir, "sel.json"), "alice", "admintoken")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"everyday","messages":[]}`))
	req.Header.Set("Authorization", "Bearer admintoken")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("service B should have served after A exhausted its models: %d", rec.Code)
	}
	if len(sawA) != 2 {
		t.Fatalf("service A should have tried both models, saw %d calls", len(sawA))
	}
}
