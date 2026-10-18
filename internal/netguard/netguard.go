// Package netguard provides the shared SSRF-safe HTTP client used by OAuth
// discovery and registry fetches: DNS is resolved first and private,
// loopback, link-local, CGNAT and multicast destinations fail before any
// packet leaves. Redirects never cross origins, and loopback is reachable
// only behind an explicit flag (tests and the authorization-server fixture).
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Client returns a bounded outbound client. allowLoopback admits
// 127.0.0.0/8 and ::1 destinations for local development and test fixtures;
// every other non-public range stays blocked regardless.
func Client(timeout time.Duration, allowLoopback bool) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Transport(allowLoopback),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// Transport resolves the host before dialing and refuses to connect when any
// resolved address is non-public — a DNS answer mixing a public and a private
// record fails closed instead of racing a rebind.
func Transport(allowLoopback bool) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("netguard: %s resolved no addresses", host)
		}
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok || blocked(addr.Unmap(), allowLoopback) {
				return nil, fmt.Errorf("netguard: %s resolves to blocked address %s", host, ip)
			}
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return base
}

func blocked(ip netip.Addr, allowLoopback bool) bool {
	if ip.IsLoopback() {
		return !allowLoopback
	}
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || cgnat.Contains(ip)
}
