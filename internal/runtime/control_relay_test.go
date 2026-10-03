package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlRelayRejectsNonOriginTargets(t *testing.T) {
	for _, target := range []string{"", "https://runtime:8080", "http://runtime:8080/admin", "http://runtime:8080/?x=1", "http://user@runtime:8080", "runtime:8080"} {
		if _, err := controlRelayHandler(target); err == nil {
			t.Fatalf("unreviewed control relay target accepted: %q", target)
		}
	}
}

func TestControlRelayForwardsOnlyToFixedUpstream(t *testing.T) {
	var sawPath, sawHost, sawOverride string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawHost, sawOverride = r.URL.RequestURI(), r.Host, r.Header.Get("X-Original-URL")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	handler, err := controlRelayHandler(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/leases?k=v", nil)
	request.Header.Set("X-Original-URL", "http://elsewhere/")
	request.Host = "spoofed.invalid"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if sawPath != "/v1/leases?k=v" {
		t.Fatalf("request path not preserved: %q", sawPath)
	}
	if sawOverride != "" {
		t.Fatal("rewrite override header reached upstream")
	}
	if sawHost == "spoofed.invalid" || sawHost == "" {
		t.Fatalf("upstream host was not rewritten to the fixed origin: %q", sawHost)
	}
}

func TestControlRelayDeniesRedirectsAndAbsoluteURIs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/escape", http.StatusFound)
	}))
	defer upstream.Close()
	handler, err := controlRelayHandler(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/self-env", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("redirect was not refused: %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	request.URL.Scheme, request.URL.Host = "http", "outside.invalid"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("absolute-URI request not refused: %d", recorder.Code)
	}
}
