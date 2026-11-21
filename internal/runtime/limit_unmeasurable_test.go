package runtime

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A service whose declared limit the relay cannot measure is disabled with
// reason limit_unmeasurable (SPEC-0092 R7.3): spend_usd has no price feed,
// so hop a never receives traffic and the ladder advances to b.
func TestUnmeasurableLimitDisablesService(t *testing.T) {
	var aCalls, bCalls atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srvB.Close()

	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n" +
		"  a:\n    allow_private: true\n    url: " + srvA.URL + "\n" +
		"  b:\n    allow_private: true\n    url: " + srvB.URL + "\n" +
		"models:\n" +
		"  default:\n    ladder:\n" +
		"      - {provider: a, model: m1, limits: [{of: spend_usd, amount: 10, per: 1d}]}\n" +
		"      - {provider: b, model: m2}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := ModelRelayRegistryHandler(registry, filepath.Join(dir, "letya.json"), "letya", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || aCalls.Load() != 0 || bCalls.Load() != 1 {
		t.Fatalf("unmeasurable service served traffic: status=%d a=%d b=%d", response.Code, aCalls.Load(), bCalls.Load())
	}
}
