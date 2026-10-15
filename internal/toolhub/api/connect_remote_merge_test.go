package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectRemoteMCP speaks the streamable transport to any injected client;
// the URL-rewriting transport proves the handshake against a loopback fixture.
func TestConnectRemoteMCPLoopback(t *testing.T) {
	t.Parallel()
	server := mcp.NewServer(&mcp.Implementation{Name: "remote-fixture", Version: "1"}, nil)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = target.Scheme, target.Host
		r2.Host = target.Host
		return http.DefaultTransport.RoundTrip(r2)
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := connectRemoteMCP(ctx, "https://mcp.example.com/mcp", client)
	if err != nil {
		t.Fatalf("streamable connect: %v", err)
	}
	session.Close()

	// A dead endpoint falls back to SSE and reports the streamable failure.
	dead := &http.Client{Transport: RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("refused")
	})}
	if _, err := connectRemoteMCP(ctx, "https://mcp.example.com/mcp", dead); err == nil {
		t.Fatal("dead endpoint must error")
	}
}
