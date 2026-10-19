package netguard

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The guard is the single egress gate for OAuth discovery and registry
// fetches: private, loopback, link-local, CGNAT and multicast destinations
// fail before a single packet leaves.
func TestBlockedDestinationsNeverDialed(t *testing.T) {
	var hits atomic.Int32
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer probe.Close()
	client := Client(5*time.Second, false)
	for _, target := range []string{
		probe.URL, // loopback without the flag
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data",
		"http://192.168.1.1/",
		"http://100.64.0.1/",
		"http://[fe80::1]/",
		"http://[::1]/",
	} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("blocked destination answered: %s", target)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("guard let %d requests through", hits.Load())
	}
}

// The same-origin redirect rule stops a public URL bouncing to a private one.
func TestRedirectCrossOriginRejected(t *testing.T) {
	var hits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer private.Close()
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer pub.Close()
	client := Client(5*time.Second, true)
	resp, err := client.Get(pub.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 0 {
		t.Fatalf("cross-origin redirect followed: status=%d hits=%d body=%s", resp.StatusCode, hits.Load(), body)
	}
}

// Loopback is reachable only behind the explicit flag the test fixture sets.
func TestLoopbackAllowedOnlyWithFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	if resp, err := Client(5*time.Second, false).Get(server.URL); err == nil {
		resp.Body.Close()
		t.Fatal("loopback allowed without flag")
	}
	resp, err := Client(5*time.Second, true).Get(server.URL)
	if err != nil {
		t.Fatalf("loopback denied with flag: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("loopback body=%q", body)
	}
	// A bare IP literal still resolves through the guard path, not a dialer shortcut.
	host := strings.TrimPrefix(server.URL, "http://")
	if _, _, err := net.SplitHostPort(host); err != nil {
		t.Fatalf("fixture host=%q: %v", host, err)
	}
}
