package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToolHubRelayOnlyForwardsMCP(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.String() != "/mcp" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("X-Original-URL") != "" {
			t.Errorf("unsafe ToolHub request: %s %v", r.URL, r.Header)
		}
		_, _ = io.WriteString(w, "mcp-ok")
	}))
	defer upstream.Close()
	handler, err := toolHubRelayHandler(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/credentials/private", "/oauth/callback", "/mcp/other", "/mcp?target=/credentials/private", "/mcp%2f"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || calls != 0 {
			t.Fatalf("unreviewed ToolHub route forwarded: %s status=%d calls=%d", path, rec.Code, calls)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer synthetic")
		req.Header.Set("X-Original-URL", "/credentials/private")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "mcp-ok" {
			t.Fatalf("MCP %s failed: %d %s", method, rec.Code, rec.Body.String())
		}
	}
	if calls != 3 {
		t.Fatalf("MCP calls=%d", calls)
	}
}

func TestToolHubRelayRejectsUpstreamRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	handler, err := toolHubRelayHandler(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Location") != "" {
		t.Fatalf("ToolHub redirect escaped: %d %v", rec.Code, rec.Header())
	}
}

func TestToolHubRelayIgnoresEnvironmentProxy(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	handler, err := toolHubRelayHandler("http://toolhub-relay-proxy-test.invalid:8090")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusBadGateway || proxyCalls != 0 {
		t.Fatalf("ToolHub relay used environment proxy: %d calls=%d", rec.Code, proxyCalls)
	}
}
