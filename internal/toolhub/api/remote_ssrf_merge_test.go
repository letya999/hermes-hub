package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRemoteAddrPublicClassifier(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		ip, err := netip.ParseAddr(raw)
		if err != nil || !RemoteAddrPublic(ip) {
			t.Fatalf("public %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254",
		"100.64.0.1", "198.18.0.1", "0.0.0.0", "255.255.255.255", "224.0.0.1",
		"::1", "fe80::1", "2001:db8::1", "::ffff:127.0.0.1", "192.0.2.1",
	} {
		ip, _ := netip.ParseAddr(raw)
		if ip.IsValid() && RemoteAddrPublic(ip) {
			t.Fatalf("private/reserved %q admitted", raw)
		}
	}
	var invalid netip.Addr
	if RemoteAddrPublic(invalid) {
		t.Fatal("invalid addr admitted")
	}
}

func TestValidateRemoteMCPEndpointSyntax(t *testing.T) {
	for _, bad := range []string{
		"http://mcp.example.com", "ftp://mcp.example.com",
		"https://user:pw@mcp.example.com", "https://mcp.example.com?q=1",
		"https://mcp.example.com#frag", "https://", "not a url", "",
	} {
		if err := ValidateRemoteMCPEndpoint(bad); err == nil {
			t.Fatalf("endpoint %q admitted", bad)
		}
	}
	if err := ValidateRemoteMCPEndpoint("https://8.8.8.8/mcp"); err != nil {
		t.Fatalf("public IP literal rejected: %v", err)
	}
}

func TestValidateRemoteMCPEndpointHostRules(t *testing.T) {
	for _, bad := range []string{
		"https://127.0.0.1/x", "https://10.0.0.5/x", "https://169.254.169.254/latest",
		"https://localhost/x", "https://toolhub/", "https://metadata.google.internal/",
		"https://foo.localhost/", "https://svc.internal/", "https://box.local/",
		"https://host.corp/", "https://node.lan/", "https://singlelabel/",
	} {
		if err := ValidateRemoteMCPEndpoint(bad); err == nil {
			t.Fatalf("endpoint %q admitted", bad)
		}
	}
	orig := ResolveRemoteIP
	defer func() { ResolveRemoteIP = orig }()
	ResolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "bad.example.com":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		case "dead.example.com":
			return nil, errors.New("nxdomain")
		case "empty.example.com":
			return nil, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if err := ValidateRemoteMCPEndpoint("https://mcp.example.com/v1"); err != nil {
		t.Fatalf("resolvable public endpoint rejected: %v", err)
	}
	if err := ValidateRemoteMCPEndpoint("https://bad.example.com/"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("private-resolving host admitted: %v", err)
	}
	if err := ValidateRemoteMCPEndpoint("https://dead.example.com/"); err == nil {
		t.Fatal("unresolvable host admitted")
	}
	if err := ValidateRemoteMCPEndpoint("https://empty.example.com/"); err == nil {
		t.Fatal("empty-resolution host admitted")
	}
}

func TestRemoteDialFence(t *testing.T) {
	orig := ResolveRemoteIP
	defer func() { ResolveRemoteIP = orig }()
	ResolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "bad.example.com" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		}
		if host == "nx.example.com" {
			return nil, errors.New("nxdomain")
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if _, err := RemoteDial(context.Background(), "tcp", "bad.example.com:443"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("private-resolving dial admitted: %v", err)
	}
	if _, err := RemoteDial(context.Background(), "tcp", "127.0.0.1:443"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("loopback literal dial admitted: %v", err)
	}
	if _, err := RemoteDial(context.Background(), "tcp", "nx.example.com:443"); err == nil {
		t.Fatal("unresolvable dial succeeded")
	}
	if _, err := RemoteDial(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("malformed addr dialed")
	}
	// public IP with nothing listening: fence passes, dial itself fails
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := RemoteDial(ctx, "tcp", "ok.example.com:1")
	if err == nil {
		conn.Close()
		t.Fatal("dial to closed port succeeded")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("public dial blocked by fence: %v", err)
	}
}

func TestCredentialHeadersContract(t *testing.T) {
	def := ToolDefinition{Credentials: []CredentialInput{
		{Name: "API_TOKEN", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "},
		{Name: "OPT_TOKEN", Delivery: "http_header", Target: "X-Opt"},
		{Name: "ENV_ONLY", Required: true},
	}}
	headers, err := CredentialHeaders(def, map[string]string{"API_TOKEN": "s3cret", "ENV_ONLY": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer s3cret" || len(headers) != 1 {
		t.Fatalf("headers: %v", headers)
	}
	if _, err := CredentialHeaders(def, map[string]string{"ENV_ONLY": "x"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing required header credential: %v", err)
	}
	if _, err := CredentialHeaders(def, map[string]string{"API_TOKEN": "bad\r\nvalue"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CRLF credential: %v", err)
	}
	headers, err = CredentialHeaders(def, map[string]string{"API_TOKEN": "tok", "OPT_TOKEN": "opt", "ENV_ONLY": "x"})
	if err != nil || headers["X-Opt"] != "opt" {
		t.Fatalf("optional header: %v %v", err, headers)
	}
}

func TestRemoteHeaderRoundTripperOriginPin(t *testing.T) {
	var sawAuth string
	base := RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		sawAuth = req.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})
	rt := RemoteHeaderRoundTripper{Base: base, Origin: "https://mcp.example.com", Headers: map[string]string{"Authorization": "Bearer tok"}}
	req, _ := http.NewRequest("GET", "https://mcp.example.com/rpc", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil || sawAuth != "Bearer tok" {
		t.Fatalf("on-origin header: %v %q", err, sawAuth)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("original request mutated")
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body: %q", body)
	}
	sawAuth = ""
	req2, _ := http.NewRequest("GET", "https://evil.example.com/rpc", nil)
	if _, err := rt.RoundTrip(req2); err != nil || sawAuth != "" {
		t.Fatalf("off-origin leaked header: %v %q", err, sawAuth)
	}
	if RequestOrigin(mustParseURL(t, "https://mcp.example.com:8443/x")) != "https://mcp.example.com:8443" ||
		RequestOrigin(mustParseURL(t, "https://mcp.example.com:443/x")) != "https://mcp.example.com" ||
		RequestOrigin(mustParseURL(t, "https://mcp.example.com/x")) != "https://mcp.example.com" {
		t.Fatal("origin derivation wrong")
	}
	// transport error passes through
	failRT := RemoteHeaderRoundTripper{Base: RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial fail")
	})}
	if _, err := failRT.RoundTrip(req); err == nil {
		t.Fatal("transport error swallowed")
	}
	if PublicHTTPClient(time.Second) == nil || DefaultRemoteHTTPClient("https://mcp.example.com", nil) == nil {
		t.Fatal("client constructors returned nil")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
