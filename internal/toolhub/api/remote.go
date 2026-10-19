package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// User-owned remote MCP endpoints are the only ToolHub traffic allowed to
// leave the private network. Everything here fails closed: HTTPS only, a
// public destination proven by DNS at validate time and again at dial time,
// no redirects, and a real MCP handshake — an HTTP 200 that does not answer
// initialize/tools/list is not MCP.

var (
	// ResolveRemoteIP is the DNS seam shared by validation and dialing. Tests
	// pin it; production uses the system resolver.
	ResolveRemoteIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	// RemoteHTTPClient builds the endpoint-bound HTTP client. Tests substitute
	// an httptest client; production gets the SSRF-guarded transport.
	RemoteHTTPClient  = DefaultRemoteHTTPClient
	headerNamePattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)
)

// remoteDenyPrefixes covers address space that is reachable-but-not-public:
// CGNAT (RFC 6598), benchmarking (RFC 2544), 6to4 relay and the protocol
// assignment blocks a resolver should never legitimately return for a remote
// MCP host. Loopback/private/link-local/unspecified/multicast are denied by
// the classifier below on top of this list.
var remoteDenyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("233.252.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::ffff:0:0/96").Masked(),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("fec0::/10"),
}

func RemoteAddrPublic(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range remoteDenyPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateRemoteMCPEndpoint admits only an HTTPS URL whose host is a public,
// non-reserved destination. Private backends (ToolHive/vMCP/compose names,
// loopback, link-local, metadata ranges) and single-label service names are
// operator config, never user chat input.
func ValidateRemoteMCPEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("%w: remote MCP URL must be HTTPS without credentials, query or fragment", ErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		// IP literals bypass the name rules entirely and stand on the
		// public-address check alone.
		if !RemoteAddrPublic(ip) {
			return fmt.Errorf("%w: remote MCP host resolves to a private address", ErrUnauthorized)
		}
		return nil
	}
	if host == "" || !strings.Contains(host, ".") {
		return fmt.Errorf("%w: remote MCP host must be a public name", ErrUnauthorized)
	}
	switch host {
	case "localhost", "toolhub", "toolhive", "vmcp", "workload-controller", "host.docker.internal", "metadata.google.internal":
		return fmt.Errorf("%w: remote MCP host is a private backend name", ErrUnauthorized)
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".corp") || strings.HasSuffix(host, ".lan") {
		return fmt.Errorf("%w: remote MCP host is a private backend name", ErrUnauthorized)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return checkRemoteHost(ctx, host)
}

func checkRemoteHost(ctx context.Context, host string) error {
	ips, err := ResolveRemoteIP(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%w: remote MCP host does not resolve", ErrUnauthorized)
	}
	for _, ip := range ips {
		if !RemoteAddrPublic(ip) {
			return fmt.Errorf("%w: remote MCP host resolves to a private address", ErrUnauthorized)
		}
	}
	return nil
}

// RemoteDial resolves the host itself and refuses to connect when every
// address is not public — the dial-time re-check behind the validate-time
// DNS answer (rebinding window).
func RemoteDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = ResolveRemoteIP(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	public := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if RemoteAddrPublic(ip) {
			public = append(public, ip)
		}
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("%w: remote MCP dial to a non-public address", ErrUnauthorized)
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var lastErr error
	for _, ip := range public {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// PublicHTTPClient shares the dial-time public-address fence with remote MCP.
// Untrusted fetches cannot use ambient proxies or redirect to internal services.
func PublicHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = RemoteDial
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type RemoteHeaderRoundTripper struct {
	Base    http.RoundTripper
	Origin  string // "https://host[:port]" the credential headers belong to
	Headers map[string]string
}

// RoundTrip injects credential headers only on requests to the admitted
// origin. Legacy SSE endpoints get a server-chosen message URL — a hostile
// server could point it at a different origin and exfiltrate the headers, so
// anything off-origin travels unauthenticated.
func (t RemoteHeaderRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	req := request
	if len(t.Headers) > 0 && req.URL != nil && RequestOrigin(req.URL) == t.Origin {
		req = request.Clone(request.Context())
		for name, value := range t.Headers {
			req.Header.Set(name, value)
		}
	}
	resp, err := t.Base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &remoteLimitedBody{r: io.LimitReader(resp.Body, RemoteMaxBodyBytes), c: resp.Body}
	return resp, nil
}

func RequestOrigin(u *url.URL) string {
	port := u.Port()
	host := u.Hostname()
	if port != "" && port != "443" {
		return u.Scheme + "://" + host + ":" + port
	}
	return u.Scheme + "://" + host
}

// RemoteMaxBodyBytes bounds every HTTP response the remote client will
// buffer: the go-sdk decodes bodies with unbounded io.ReadAll, so the cap has
// to live in transport — far below memory pressure, far above the largest
// legitimate tools/list or call result (OutputBytes governs those).
const RemoteMaxBodyBytes = 8 << 20

type remoteLimitedBody struct {
	r io.Reader
	c io.Closer
}

func (b *remoteLimitedBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *remoteLimitedBody) Close() error               { return b.c.Close() }

// DefaultRemoteHTTPClient binds the SSRF guard into transport and redirect
// policy. A remote MCP endpoint never legitimately redirects: following one
// would let a reviewed URL launder a private or alternate destination.
func DefaultRemoteHTTPClient(endpoint string, headers map[string]string) *http.Client {
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           RemoteDial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	origin := ""
	if u, err := url.Parse(endpoint); err == nil {
		origin = RequestOrigin(u)
	}
	return &http.Client{
		Timeout:       45 * time.Second,
		Transport:     RemoteHeaderRoundTripper{Base: base, Origin: origin, Headers: headers},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CredentialHeaders maps admitted credential values onto the HTTP headers the
// immutable definition declares. Only inputs with delivery "http_header"
// produce headers; a required one missing from the injected environment fails
// closed instead of silently authenticating nothing.
func CredentialHeaders(definition ToolDefinition, env map[string]string) (map[string]string, error) {
	headers := map[string]string{}
	for _, input := range definition.Credentials {
		if input.Delivery != "http_header" {
			continue
		}
		value, ok := env[input.Name]
		if !ok || value == "" {
			if input.Required {
				return nil, fmt.Errorf("%w: credential %s not injected", ErrUnauthorized, input.Name)
			}
			continue
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%w: credential %s cannot be a header", ErrInvalid, input.Name)
		}
		headers[input.Target] = input.Prefix + value
	}
	return headers, nil
}
