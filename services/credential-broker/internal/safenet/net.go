// Package safenet supplies fixed-authority HTTPS clients. DNS results are checked
// at connect time; environment proxies and redirects are intentionally disabled.
package safenet

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrDestination = errors.New("destination not permitted")

type Policy struct {
	// Private service endpoints require an explicit, narrow CIDR exception.
	AllowedCIDRs []netip.Prefix
	TLSConfig    *tls.Config
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
}

func Allowed(ip netip.Addr, exceptions []netip.Prefix) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, p := range exceptions {
		if p.Contains(ip) {
			return true
		}
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func ParseExceptions(v []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range v {
		p, e := netip.ParsePrefix(s)
		if e != nil || (p.Addr().Is4() && p.Bits() < 24) || (p.Addr().Is6() && p.Bits() < 64) {
			return nil, ErrDestination
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

type fixedTransport struct {
	inner     *http.Transport
	authority string
}

func (f fixedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != f.authority || r.URL.User != nil || r.URL.Fragment != "" {
		return nil, ErrDestination
	}
	return f.inner.RoundTrip(r)
}
func New(base string, p Policy) (*http.Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || strings.HasSuffix(u.Hostname(), ".") {
		return nil, ErrDestination
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	if p.TLSConfig != nil {
		tlsConfig = p.TLSConfig.Clone()
		tlsConfig.ServerName = u.Hostname()
		if tlsConfig.InsecureSkipVerify {
			return nil, ErrDestination
		}
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy: nil, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 16,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true,
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		h, pp, e := net.SplitHostPort(address)
		if e != nil || h != u.Hostname() || pp != port {
			return nil, ErrDestination
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		if e != nil || len(ips) == 0 {
			return nil, ErrDestination
		}
		for _, ip := range ips {
			if !Allowed(ip, p.AllowedCIDRs) {
				return nil, ErrDestination
			}
		}
		var last error
		for _, ip := range ips {
			c, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), pp))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: fixedTransport{tr, u.Host}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (f fixedTransport) CloseIdleConnections() { f.inner.CloseIdleConnections() }
