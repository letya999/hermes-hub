package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A forged /v1/runs body can self-sign hosted_room_dispatch and
// _room_execution_policy (the upstream digest is a hash, not a signature) to
// replace the enabled toolset list without the managed denylist. The relay is
// the only inbound path, so it must refuse those keys before Hermes sees them.
func TestControlRelayDeniesForgedRoomDispatch(t *testing.T) {
	var forwarded []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	handler, err := controlRelayHandler(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string, contentType string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	for _, body := range []string{
		`{"input":"hi","hosted_room_dispatch":{},"_room_execution_policy":{"enabled_toolsets":["terminal"]}}`,
		`{"input":"hi","hosted_room_dispatch":{}}`,
		`{"input":"hi","_room_execution_policy":{}}`,
		`{"input":"hi","hosted_room_dispatch":null}`,
	} {
		if recorder := post(body, "application/json"); recorder.Code != http.StatusForbidden {
			t.Fatalf("forged room dispatch body admitted: %d %s", recorder.Code, body)
		}
	}
	// Escape obfuscation decodes to the exact denied key in both this check and
	// upstream's json.loads, so it must not sneak past either boundary.
	if recorder := post(`{"input":"hi","hosted_room_dispat\u0063h":{}}`, "application/json"); recorder.Code != http.StatusForbidden {
		t.Fatalf("escaped denied key admitted: %d", recorder.Code)
	}
	if recorder := post(`{"input":"hi","tools":[]}`, "application/json"); recorder.Code != http.StatusTeapot {
		t.Fatalf("ordinary run body rejected: %d", recorder.Code)
	}
	if string(forwarded) != `{"input":"hi","tools":[]}` {
		t.Fatalf("clean body not forwarded intact: %s", forwarded)
	}
	if recorder := post("not json at all", "text/plain"); recorder.Code != http.StatusTeapot {
		t.Fatalf("non-JSON body rejected: %d", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/x", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTeapot {
		t.Fatalf("bodyless request rejected: %d", recorder.Code)
	}
	// An over-limit body cannot be proven free of the denied keys.
	oversize := `{"input":"` + strings.Repeat("x", controlBodyInspectLimit) + `"}`
	if recorder := post(oversize, "application/json"); recorder.Code != http.StatusForbidden {
		t.Fatalf("uninspectable oversize body admitted: %d", recorder.Code)
	}
}
