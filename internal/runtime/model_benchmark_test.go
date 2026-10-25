package runtime

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The same localhost provider/payload isolates relay overhead from inference.
func BenchmarkModelRelaySuccess(b *testing.B) {
	previous := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(previous) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"fixture"}}]}`)
	}))
	defer upstream.Close()
	hops, err := parseModelHops("p=" + upstream.URL)
	if err != nil {
		b.Fatal(err)
	}
	static, err := modelRelayHandler(hops, "fixture")
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "models.yaml")
	config := "providers:\n  p:\n    url: " + upstream.URL + "\n    allow_private: true\nmodels:\n  fixture:\n    ladder: [{provider: p, model: fixture}]\n"
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		b.Fatal(err)
	}
	registry, err := modelRelayRegistryHandler(path, filepath.Join(b.TempDir(), "selection.json"), "alice", "")
	if err != nil {
		b.Fatal(err)
	}
	staticServer := httptest.NewServer(static)
	defer staticServer.Close()
	registryServer := httptest.NewServer(registry)
	defer registryServer.Close()
	payload := `{"model":"fixture","messages":[{"role":"user","content":"` + strings.Repeat("x", 32<<10) + `"}]}`
	for _, target := range []struct{ name, url string }{{"direct", upstream.URL}, {"static", staticServer.URL}, {"registry", registryServer.URL}} {
		b.Run(target.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				response, err := http.Post(target.url+"/v1/chat/completions", "application/json", strings.NewReader(payload))
				if err != nil {
					b.Fatal(err)
				}
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if err != nil || response.StatusCode != 200 {
					b.Fatalf("benchmark request failed: status=%d err=%v", response.StatusCode, err)
				}
			}
		})
	}
}
