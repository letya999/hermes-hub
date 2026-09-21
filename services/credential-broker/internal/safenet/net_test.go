package safenet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIPPolicy(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.1.2", "192.168.1.1", "169.254.169.254", "0.0.0.0", "::", "224.0.0.1", "ff00::1", "::ffff:127.0.0.1", "100.64.0.1", "192.0.2.1", "198.18.1.1", "2001:db8::1", "64:ff9b::7f00:1"} {
		if Allowed(netip.MustParseAddr(s), nil) {
			t.Fatal(s)
		}
	}
	if Allowed(netip.Addr{}, nil) {
		t.Fatal("invalid")
	}
	if !Allowed(netip.MustParseAddr("8.8.8.8"), nil) || !Allowed(netip.MustParseAddr("2606:4700:4700::1111"), nil) {
		t.Fatal("public address")
	}
	extra, e := ParseExceptions([]string{"127.0.0.1/32", "fd00::/64"})
	if e != nil || !Allowed(netip.MustParseAddr("127.0.0.1"), extra) {
		t.Fatal(e)
	}
	for _, s := range []string{"0.0.0.0/0", "::/0", "bad"} {
		if _, e := ParseExceptions([]string{s}); e == nil {
			t.Fatal("broad exception")
		}
	}
	if Allowed(netip.MustParseAddr("169.254.169.254"), []netip.Prefix{netip.MustParsePrefix("169.254.169.254/32")}) {
		t.Fatal("metadata bypass")
	}
}
func TestFixedTLSNoRedirectNoEnvProxy(t *testing.T) {
	calls := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://example.invalid/", 302)
			return
		}
		w.WriteHeader(204)
	}))
	defer up.Close()
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	p := Policy{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLSConfig: &tls.Config{RootCAs: pool}}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	c, e := New(up.URL, p)
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseIdleConnections()
	res, e := c.Get(up.URL + "/ok")
	if e != nil {
		t.Fatal(e)
	}
	_ = res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatal(res.Status)
	}
	res, e = c.Get(up.URL + "/redirect")
	if e != nil || res.StatusCode != 302 {
		t.Fatal("redirect followed", e)
	}
	_ = res.Body.Close()
	if calls != 2 {
		t.Fatal(calls)
	}
	if _, e = c.Get("https://example.invalid/secret"); e == nil {
		t.Fatal("authority escape")
	}
	if _, e = c.Get("http://127.0.0.1/"); e == nil {
		t.Fatal("cleartext")
	}
	d, e := New(up.URL, Policy{TLSConfig: p.TLSConfig})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.Get(up.URL); e == nil {
		t.Fatal("private IP without exception")
	}
	for _, raw := range []string{"http://x", "https://u:p@x", "https://x?token=a", "https://x.", "://"} {
		if _, e := New(raw, p); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := New(up.URL, Policy{TLSConfig: &tls.Config{InsecureSkipVerify: true}}); e == nil {
		t.Fatal("TLS verification disabled")
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", up.URL, nil)
	req.URL.Fragment = "evil"
	if _, e = c.Do(req); e == nil {
		t.Fatal("fragment")
	}
}
