package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SPEC-0053 R1.4-R1.6: /v1/health carries the tool surface, missing names and
// model status so the supervisor can observe them without a restart.
func TestHealthReportsSurfaceAndModel(t *testing.T) {
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"readiness": map[string]string{"status": "ok"},
			"platforms": map[string]any{"telegram": map[string]string{"state": "connected"}},
		})
	}))
	defer native.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(native.URL, "http://"), ":")
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	t.Setenv("HUB_RUNTIME_AUTH", "auth")

	previous := surfaceProbe
	surfaceProbe = func(ctx context.Context) probeResult {
		return probeResult{surface: "degraded", missing: []string{"image_gen"}, reasons: map[string]string{"hub": "child_env_unresolved:HUB_POLICY_VERSION"}, model: "model_not_found"}
	}
	t.Cleanup(func() { surfaceProbe = previous })

	request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	request.Header.Set("Authorization", "Bearer auth")
	response := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health returned %d: %s", response.Code, response.Body)
	}
	var health RuntimeHealth
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.ToolSurface != "degraded" {
		t.Fatalf("tool_surface = %q", health.ToolSurface)
	}
	if len(health.MissingTools) != 1 || health.MissingTools[0] != "image_gen" {
		t.Fatalf("missing_tools = %v", health.MissingTools)
	}
	if health.ModelStatus != "model_not_found" {
		t.Fatalf("model_status = %q", health.ModelStatus)
	}
	if health.ToolReasons["hub"] != "child_env_unresolved:HUB_POLICY_VERSION" {
		t.Fatalf("tool_reasons = %v", health.ToolReasons)
	}
}

// SPEC-0053 R1.5/R1.6: a degraded tool surface or a missing model downgrades
// readiness to "degraded" so the supervisor and /status see one verdict; an
// unavailable Hermes keeps the stronger signal.
func TestHealthDegradesReadinessOnBadSurface(t *testing.T) {
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"readiness": map[string]string{"status": "ok"},
		})
	}))
	defer native.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(native.URL, "http://"), ":")
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	t.Setenv("HUB_RUNTIME_AUTH", "auth")

	previous := surfaceProbe
	t.Cleanup(func() { surfaceProbe = previous })
	call := func() RuntimeHealth {
		request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		request.Header.Set("Authorization", "Bearer auth")
		response := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(response, request)
		var health RuntimeHealth
		if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
			t.Fatal(err)
		}
		return health
	}

	surfaceProbe = func(ctx context.Context) probeResult {
		return probeResult{surface: "degraded", missing: []string{"terminal"}, model: "ok"}
	}
	if health := call(); health.HermesReadiness != "degraded" {
		t.Fatalf("readiness with degraded surface = %q", health.HermesReadiness)
	}
	surfaceProbe = func(ctx context.Context) probeResult {
		return probeResult{surface: "ok", model: "model_not_found"}
	}
	if health := call(); health.HermesReadiness != "degraded" {
		t.Fatalf("readiness with model_not_found = %q", health.HermesReadiness)
	}
	surfaceProbe = func(ctx context.Context) probeResult {
		return probeResult{surface: "ok", model: "ok"}
	}
	if health := call(); health.HermesReadiness != "ready" {
		t.Fatalf("readiness with clean probe = %q", health.HermesReadiness)
	}
}

func TestRuntimeDiagnosticsServerBoundsHeaderReads(t *testing.T) {
	server := runtimeDiagnosticsServer("0.0.0.0:0", nil)
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("diagnostics server has no header timeout")
	}
}
