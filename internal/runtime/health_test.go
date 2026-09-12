package runtime

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthSeparatesNativeReadinessAndConnectionsAndDropsPrivateDetails(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	state := "ok"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/detailed" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong native health request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"readiness": map[string]any{"status": state, "detail": "secret-canary"}, "platforms": map[string]any{"private-canary": map[string]any{"state": "connected", "token": "secret-canary"}, "telegram": map[string]string{"state": "error", "detail": "secret-canary"}}, "config": "prompt-canary"})
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	call := func() RuntimeHealth {
		r := httptest.NewRequest("GET", "/v1/health", nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(w, r)
		var h RuntimeHealth
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &h) != nil {
			t.Fatalf("health: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "canary") {
			t.Fatal("health exposed native details")
		}
		return h
	}
	if h := call(); h.HermesReadiness != "ready" || h.ConnectorHealth != "degraded" || h.Connected != 1 || h.Degraded != 1 || h.ExternalConnections != "not_probed" {
		t.Fatalf("health=%+v", h)
	}
	state = "degraded"
	if h := call(); h.HermesReadiness != "unavailable" || h.ConnectorHealth != "degraded" {
		t.Fatalf("health=%+v", h)
	}
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(w, httptest.NewRequest(method, "/v1/health", nil))
		if w.Code != 401 {
			t.Fatal("unauthed health exposed")
		}
	}
}
func TestHealthRejectsUnboundedOrUnavailableNativeBody(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	body := "not json"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	for _, b := range []string{body, `{"readiness":{"status":"ok"},"detail":"` + strings.Repeat("x", 129*1024) + `"}`} {
		body = b
		r := httptest.NewRequest("GET", "/v1/health", nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatal("invalid native health accepted")
		}
	}
}

func TestHealthUnavailableTransportDoesNotExposePrivateConfiguration(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	api.Close()
	t.Setenv("HUB_HERMES_API_PORT", port)
	for _, nativeHost := range []string{host, "[private-canary"} {
		t.Setenv("HUB_HERMES_API_HOST", nativeHost)
		r := httptest.NewRequest("GET", "/v1/health", nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(w, r)
		if w.Code != 503 || strings.Contains(w.Body.String(), "canary") {
			t.Fatalf("transport/configuration failure exposed: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestHealthUnknownAndHealthyPlatformsRemainAggregateOnly(t *testing.T) {
	for _, state := range []string{"future-state", "connected", "disabled"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("HUB_RUNTIME_AUTH", "secret")
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"readiness": map[string]string{"status": "ok"}, "platforms": map[string]any{"private-canary": map[string]string{"status": state, "token": "secret-canary"}}})
			}))
			defer api.Close()
			host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
			t.Setenv("HUB_HERMES_API_HOST", host)
			t.Setenv("HUB_HERMES_API_PORT", port)
			r := httptest.NewRequest("GET", "/v1/health", nil)
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			runtimeHandler().ServeHTTP(w, r)
			var health RuntimeHealth
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &health) != nil || strings.Contains(w.Body.String(), "canary") || health.ExternalConnections != "not_probed" {
				t.Fatalf("health leaked or claimed provider integration: %d %s", w.Code, w.Body.String())
			}
			expected := map[string]string{"future-state": "unknown", "connected": "healthy", "disabled": "not_configured"}[state]
			if health.ConnectorHealth != expected {
				t.Fatalf("unexpected platform aggregate: %+v", health)
			}
		})
	}
}
