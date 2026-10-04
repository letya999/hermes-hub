package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelRelayOnlyForwardsReviewedChatRoute(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.String() != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("X-HTTP-Method-Override") != "" || r.Header.Get("X-Original-URL") != "" {
			t.Errorf("unsafe upstream request: %s %s %v", r.Method, r.URL, r.Header)
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(upstream.URL, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/v0/management/api-keys", "/v8/management/config", "/management.html", "/v1/models", "/v1/chat/completions?target=/v0/management", "/v1/chat%2fcompletions"} {
		req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusNotFound || calls != 0 {
			t.Fatalf("unreviewed route reached upstream: %s status=%d calls=%d", endpoint, response.Code, calls)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodConnect} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v1/chat/completions", nil))
		if response.Code != http.StatusNotFound || calls != 0 {
			t.Fatalf("unreviewed method reached upstream: %s", method)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Authorization", "Bearer synthetic")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	req.Header.Set("X-Original-URL", "/v0/management")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 || response.Body.String() != `{"choices":[]}` {
		t.Fatalf("reviewed chat route failed: status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	for _, body := range []string{`{}`, `{"model":"other"}`, `{"model":"synthetic","model":"synthetic"}`, `{"model":"synthetic","Model":"other"}`, `{"model":null}`, `{"model":"synthetic"} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden || calls != 1 {
			t.Fatalf("unapproved model reached upstream: %q status=%d calls=%d", body, response.Code, calls)
		}
	}
	tooLarge := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	tooLarge.Header.Set("Content-Type", "application/json")
	tooLarge.ContentLength = (32 << 20) + 1
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tooLarge)
	if response.Code != http.StatusRequestEntityTooLarge || calls != 1 {
		t.Fatal("oversized model request reached upstream")
	}
}

func TestModelRelayRejectsUpstreamRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	handler, err := modelRelayHandler(upstream.URL, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadGateway || response.Header().Get("Location") != "" {
		t.Fatalf("upstream redirect escaped: %d %v", response.Code, response.Header())
	}
}

func TestModelRelayIgnoresEnvironmentProxy(t *testing.T) {
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
	handler, err := modelRelayHandler("http://model-relay-proxy-test.invalid:8317", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"synthetic"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || proxyCalls != 0 {
		t.Fatalf("relay used environment proxy: status=%d proxy=%d", response.Code, proxyCalls)
	}
}
