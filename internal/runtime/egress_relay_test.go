package runtime

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEgressAllowed(t *testing.T) {
	allow := parseEgressHosts("api.keenable.ai,*.duckduckgo.com, duckduckgo.com")
	for _, host := range []string{"api.keenable.ai", "duckduckgo.com", "html.duckduckgo.com", "lite.duckduckgo.com"} {
		if !egressAllowed(allow, host) {
			t.Fatalf("allowlisted host denied: %s", host)
		}
	}
	for _, host := range []string{"", "evil.com", "keenable.ai", "notkeenable.ai", "api.keenable.ai.evil.com", "duckduckgo.com.evil.com"} {
		if egressAllowed(allow, host) {
			t.Fatalf("non-allowlisted host allowed: %s", host)
		}
	}
}

func TestEgressRelayDeniesUnlisted(t *testing.T) {
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts("api.keenable.ai")))
	defer relay.Close()
	for _, target := range []string{"http://evil.com/", "http://communication-hub:8081/v1/routines", "/relative"} {
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(relay.URL, "http://"), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", target)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("unlisted host status %d: %s", resp.StatusCode, target)
		}
	}
}

func TestEgressRelayForwardsListedHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "hit")
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()
	host := strings.TrimPrefix(upstream.URL, "http://")
	hostname, _, _ := net.SplitHostPort(host)
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts(hostname)))
	defer relay.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(relay.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/path HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.URL, host)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" || resp.Header.Get("X-Upstream") != "hit" {
		t.Fatalf("forward failed: %d %q", resp.StatusCode, body)
	}
}

func TestEgressRelayConnect(t *testing.T) {
	// A real CONNECT tunnel to a local listener on 443 is impractical in a
	// test; assert the policy gate instead: non-443 and unlisted are denied,
	// listed:443 attempts to dial (fails upstream only).
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts("api.keenable.ai")))
	defer relay.Close()
	addr := strings.TrimPrefix(relay.URL, "http://")
	for _, target := range []string{"evil.com:443", "api.keenable.ai:22"} {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		conn.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("CONNECT %s status %d", target, resp.StatusCode)
		}
	}
	// Listed host on 443 attempts the upstream dial; api.keenable.ai resolves
	// but is unreachable/denied at read time — accept any non-403 outcome.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT api.keenable.ai:443 HTTP/1.1\r\nHost: api.keenable.ai:443\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Fatal("allowlisted CONNECT denied")
		}
	}
	// Timeout/dial errors also prove the request passed the allowlist gate.
}

func TestRunEgressRelayFailsOnBadListen(t *testing.T) {
	t.Setenv("HUB_EGRESS_RELAY_LISTEN", "127.0.0.1:not-a-port")
	if err := runEgressRelay(); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}

func TestParseEgressHostsDropsMalformed(t *testing.T) {
	allow := parseEgressHosts(" , api.keenable.ai, :443, *.ok.dev, *.bad:443/x, http://x ")
	if len(allow) != 2 || !allow["api.keenable.ai"] || !allow["*.ok.dev"] {
		t.Fatalf("malformed allowlist entries not dropped: %v", allow)
	}
}
