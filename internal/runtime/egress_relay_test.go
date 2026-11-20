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

// An exact allowlist entry must not widen to subdomains: only an explicit
// `*.` entry may admit a suffix walk.
func TestEgressExactHostDoesNotAllowSubdomains(t *testing.T) {
	allow := parseEgressHosts("api.example.com")
	if egressAllowed(allow, "attacker.api.example.com") {
		t.Fatal("exact host entry admitted a subdomain")
	}
	if egressAllowed(allow, "sub.api.example.com") {
		t.Fatal("exact host entry admitted a nested subdomain")
	}
	if !egressAllowed(allow, "api.example.com") {
		t.Fatal("exact host entry denied")
	}
	wildcard := parseEgressHosts("*.example.com")
	if !wildcard["*.example.com"] {
		t.Fatal("wildcard entry dropped by parser")
	}
	if !egressAllowed(wildcard, "attacker.api.example.com") {
		t.Fatal("wildcard entry denied a subdomain")
	}
	if egressAllowed(wildcard, "example.com") {
		t.Fatal("wildcard entry admitted the bare apex")
	}
}

func TestEgressRelayDeniesUnlisted(t *testing.T) {
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts("api.keenable.ai"), ""))
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
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts(hostname), ""))
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

// Long control-plane calls (skill-git installs span sequential bounded
// cells) must not trip the relay's upstream header timeout: the bound has
// to sit above that budget and be observable as a real 502 when shrunk.
func TestEgressRelaySurvivesSlowControlUpstream(t *testing.T) {
	if egressRelayResponseTimeout < 10*time.Minute {
		t.Fatalf("response timeout %v cannot cover sequential skill-git cells", egressRelayResponseTimeout)
	}
	defer func(prev time.Duration) { egressRelayResponseTimeout = prev }(egressRelayResponseTimeout)
	egressRelayResponseTimeout = 50 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	defer upstream.Close()
	host := strings.TrimPrefix(upstream.URL, "http://")
	hostname, _, _ := net.SplitHostPort(host)
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts(hostname), ""))
	defer relay.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(relay.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/slow HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.URL, host)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("a shrunk timeout must surface 502, got %d", resp.StatusCode)
	}
}

func TestEgressRelayConnect(t *testing.T) {
	// A real CONNECT tunnel to a local listener on 443 is impractical in a
	// test; assert the policy gate instead: non-443 and unlisted are denied,
	// listed:443 attempts to dial (fails upstream only).
	relay := httptest.NewServer(egressRelayHandler(parseEgressHosts("api.keenable.ai"), ""))
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

func TestEgressRelaySishTargetOnly(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			_, _ = io.WriteString(conn, "ok")
			_ = conn.Close()
		}
	}()
	target := upstream.Addr().String()
	relay := httptest.NewServer(egressRelayHandler(nil, target))
	defer relay.Close()
	request := func(method, address string) (*http.Response, *bufio.Reader, net.Conn) {
		t.Helper()
		conn, dialErr := net.DialTimeout("tcp", strings.TrimPrefix(relay.URL, "http://"), 2*time.Second)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		host := address
		if method != http.MethodConnect {
			host = "test.invalid"
		}
		fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\n", method, address, host)
		reader := bufio.NewReader(conn)
		response, readErr := http.ReadResponse(reader, nil)
		if readErr != nil {
			conn.Close()
			t.Fatal(readErr)
		}
		return response, reader, conn
	}
	for _, address := range []string{"127.0.0.1:22", "example.invalid:2222"} {
		response, _, conn := request(http.MethodConnect, address)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("CONNECT %s status %d", address, response.StatusCode)
		}
		response.Body.Close()
		conn.Close()
	}
	response, _, conn := request(http.MethodGet, "http://"+target+"/")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("ordinary HTTP gained sish-only host: %d", response.StatusCode)
	}
	response.Body.Close()
	conn.Close()
	response, reader, conn := request(http.MethodConnect, target)
	defer conn.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("declared CONNECT status %d", response.StatusCode)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("declared CONNECT payload %q: %v", buf, err)
	}
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
