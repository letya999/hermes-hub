package runtime

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A cliproxy service holding an OAuth session reports per-session quota
// through the management API (SPEC-0092 R7.3): a provider_quota limit is
// measurable there, and a session whose remaining window fraction is below
// the limit's headroom parks the hop and advances the ladder.
func TestProviderQuotaEnforcedViaTelemetry(t *testing.T) {
	var aCalls, bCalls, mgmtCalls atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"provider":"antigravity","auth_index":"a1","email":"a@x","project_id":"p"}]}`))
		case "/v0/management/api-call":
			mgmtCalls.Add(1)
			_, _ = w.Write([]byte(`{"status_code":200,"body":"{\"groups\":[{\"displayName\":\"gemini\",\"buckets\":[{\"window\":\"1d\",\"resetTime\":\"2030-01-01T00:00:00Z\",\"remainingFraction\":0.3}]}]}"}`))
		default:
			aCalls.Add(1)
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srvB.Close()

	t.Setenv("HUB_CLIPROXY_MGMT_KEY", "mgmt-fixture")
	dir := t.TempDir()
	registry := filepath.Join(dir, "models.yaml")
	content := "providers:\n" +
		"  a:\n    allow_private: true\n    url: " + srvA.URL + "\n    via: cliproxy\n    upstream_kind: oauth-session\n" +
		"  b:\n    allow_private: true\n    url: " + srvB.URL + "\n" +
		"models:\n" +
		"  default:\n    ladder:\n" +
		"      - {provider: a, model: m1, limits: [{of: provider_quota, amount: 50, unit: percent, per: 1d, clock: provider}]}\n" +
		"      - {provider: b, model: m2}\n"
	if err := os.WriteFile(registry, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := modelRelayRegistryHandler(registry, filepath.Join(dir, "letya.json"), "letya", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, chatRequest())
	if response.Code != http.StatusOK || aCalls.Load() != 0 || bCalls.Load() != 1 {
		t.Fatalf("quota-exhausted service served traffic: status=%d a=%d b=%d", response.Code, aCalls.Load(), bCalls.Load())
	}
	if mgmtCalls.Load() == 0 {
		t.Fatal("provider_quota limit skipped without ever reading the session's telemetry")
	}
}
