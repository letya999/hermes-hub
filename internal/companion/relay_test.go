package companion

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOAuthRelayForwardsOnlyCallback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2callback" {
			t.Errorf("target path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("authorized"))
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
	go func() { done <- RunOAuthRelay(ctx, fmt.Sprintf("127.0.0.1:%d", port), target.URL+"/oauth2callback") }()
	client := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	for i := 0; i < 20; i++ {
		response, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth2callback?code=x&state=y", port))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("OAuth relay request: %v status=%v", err, response)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "authorized" {
		t.Fatalf("OAuth relay body: %q", body)
	}
	for _, bad := range []string{"/other", "/oauth2callback/extra"} {
		badResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, bad))
		if err != nil || badResp.StatusCode != http.StatusNotFound {
			t.Fatalf("OAuth relay path %q: %v status=%v", bad, err, badResp)
		}
		badResp.Body.Close()
	}
	postResp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/oauth2callback", port), "text/plain", strings.NewReader("x"))
	if err != nil || postResp.StatusCode != http.StatusNotFound {
		t.Fatalf("OAuth relay method: %v status=%v", err, postResp)
	}
	postResp.Body.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OAuth relay did not stop")
	}
}

func TestOAuthRelayRejectsUnsafeTarget(t *testing.T) {
	for _, target := range []string{"", "https://example.com/oauth2callback", "http://example.com/mcp", "http://example.com/oauth2callback?x=1", "://bad"} {
		if err := RunOAuthRelay(context.Background(), "127.0.0.1:0", target); err == nil {
			t.Fatalf("unsafe OAuth relay target accepted: %q", target)
		}
	}
}

func TestOAuthExecRelayRequiresStateAndCode(t *testing.T) {
	if err := RunOAuthExecRelay(context.Background(), "", "container"); err == nil {
		t.Fatal("OAuth exec relay without listen accepted")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOAuthExecRelay(ctx, fmt.Sprintf("127.0.0.1:%d", port), "missing-container") }()
	client := &http.Client{Timeout: 2 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var response *http.Response
	for i := 0; i < 20; i++ {
		response, err = client.Get(base + "/oauth2callback?state=s&code=c")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("OAuth exec relay request: %v", err)
	}
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("OAuth exec relay with missing container: status=%d", response.StatusCode)
	}
	response.Body.Close()
	for _, path := range []string{"/other?state=s&code=c", "/oauth2callback?state=s", "/oauth2callback?code=c"} {
		badResp, err := client.Get(base + path)
		if err != nil || badResp.StatusCode != http.StatusNotFound {
			t.Fatalf("OAuth exec relay path %q: %v status=%v", path, err, badResp)
		}
		badResp.Body.Close()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OAuth exec relay did not stop")
	}
}

func TestOAuthForwardRequiresStateAndCode(t *testing.T) {
	var output strings.Builder
	for _, input := range []string{"", "not-a-query", "state=s", "code=c"} {
		if err := OAuthForward(context.Background(), strings.NewReader(input), &output); err == nil {
			t.Fatalf("invalid OAuth forward input accepted: %q", input)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:3500")
	if err != nil {
		t.Skip("127.0.0.1:3500 unavailable")
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2callback" || r.URL.Query().Get("state") != "s" || r.URL.Query().Get("code") != "c" {
			t.Errorf("forwarded request: %s", r.URL)
		}
		_, _ = w.Write([]byte("forwarded"))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	output.Reset()
	if err := OAuthForward(context.Background(), strings.NewReader("state=s&code=c"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "forwarded" {
		t.Fatalf("OAuth forward output: %q", output.String())
	}
}
