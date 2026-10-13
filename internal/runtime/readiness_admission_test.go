package runtime

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyWaitsForInitializedNativeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, status, gateway, api string
		want                       int
	}{
		{"ready", "ok", "ok", "connected", 200},
		{"noncritical-degradation", "degraded", "ok", "connected", 200},
		{"api-starting", "ok", "ok", "starting", 503},
		{"gateway-starting", "ok", "pending", "connected", 503},
		{"unavailable", "unavailable", "ok", "connected", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health/detailed" || r.Header.Get("Authorization") != "Bearer native-secret" {
					t.Error("wrong readiness contract")
					w.WriteHeader(400)
					return
				}
				_, _ = fmt.Fprintf(w, `{"readiness":{"status":%q,"checks":{"gateway":{"status":%q,"state":"running"}}},"platforms":{"api_server":{"state":%q}}}`, tc.status, tc.gateway, tc.api)
			}))
			defer native.Close()
			host, port, _ := net.SplitHostPort(native.Listener.Addr().String())
			t.Setenv("HUB_HERMES_API_HOST", host)
			t.Setenv("HUB_HERMES_API_PORT", port)
			t.Setenv("API_SERVER_KEY", "native-secret")
			t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			request.Header.Set("Authorization", "Bearer runtime-secret")
			response := httptest.NewRecorder()
			(&runtimeHTTP{}).ready(response, request)
			if response.Code != tc.want {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
}
