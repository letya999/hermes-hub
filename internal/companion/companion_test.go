package companion

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInternalCompanionRejectsBrowserOrigin(t *testing.T) {
	called := 0
	h := internalCompanion(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusNoContent) }))
	blocked := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	blocked.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, blocked)
	if recorder.Code != http.StatusForbidden || called != 0 {
		t.Fatalf("origin: status=%d called=%d", recorder.Code, called)
	}
	ok := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, ok)
	if recorder.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("internal companion: status=%d called=%d", recorder.Code, called)
	}
}

func TestUnauthenticatedCompanionRequiresInternalListener(t *testing.T) {
	file := filepath.Join(t.TempDir(), "companion.yaml")
	if err := os.WriteFile(file, []byte("listen: 127.0.0.1:8765\ncommand: [/bin/true]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), file); err == nil {
		t.Fatal("loopback unauthenticated companion accepted")
	}
	if err := validateCompanionAuth(Config{Listen: "bad", TokenEnv: "T"}, ""); err == nil {
		t.Fatal("invalid companion listen accepted")
	}
	if err := validateCompanionAuth(Config{Listen: "0.0.0.0:8765"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := validateCompanionAuth(Config{Listen: "127.0.0.1:8765", TokenEnv: "T"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
}

func TestAuthAndOrigin(t *testing.T) {
	called := 0
	h := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }), "owner-token")
	for _, tc := range []struct {
		token, origin string
		status        int
	}{{"", "", 401}, {"Bearer other", "", 401}, {"Bearer owner-token", "https://evil.example", 403}, {"Bearer owner-token", "", 204}} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d want %d", w.Code, tc.status)
		}
	}
	if called != 1 {
		t.Fatal(called)
	}
}

func TestRelayForwardsOnlyMCPPath(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("target path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	token := strings.Repeat("r", 32)
	go func() { done <- RunRelay(ctx, fmt.Sprintf("127.0.0.1:%d", port), target.URL+"/mcp", token) }()
	client := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	for i := 0; i < 20; i++ {
		request, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/mcp", port), strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+token)
		response, err = client.Do(request)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("relay request: %v status=%v", err, response)
	}
	response.Body.Close()
	unauthorized, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/mcp", port), "application/json", strings.NewReader("{}"))
	if err != nil || unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("relay auth: %v status=%v", err, unauthorized)
	}
	unauthorized.Body.Close()
	badReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/other", port), strings.NewReader("{}"))
	badReq.Header.Set("Authorization", "Bearer "+token)
	bad, err := client.Do(badReq)
	if err != nil || bad.StatusCode != http.StatusNotFound {
		t.Fatalf("relay path: %v status=%v", err, bad)
	}
	bad.Body.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop")
	}
}

func TestRelayRejectsUnsafeTarget(t *testing.T) {
	for _, target := range []string{"", "https://example.com/mcp", "http://example.com/other", "http://example.com/mcp?x=1", "://bad"} {
		if err := RunRelay(context.Background(), "127.0.0.1:0", target, strings.Repeat("r", 32)); err == nil {
			t.Fatalf("unsafe relay target accepted: %q", target)
		}
	}
	if err := RunRelay(context.Background(), "127.0.0.1:0", "http://127.0.0.1:9/mcp", "short"); err == nil {
		t.Fatal("relay without bearer token accepted")
	}
}
