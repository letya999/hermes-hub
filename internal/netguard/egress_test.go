package netguard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEgressProxyParsing(t *testing.T) {
	for _, raw := range []string{"http://proxy.corp:3128", "https://proxy.corp:8443", "http://user:pass@proxy.corp:3128", " http://proxy.corp "} {
		t.Setenv("HUB_EGRESS_PROXY", raw)
		u, err := EgressProxy()
		if err != nil || u == nil || u.Host == "" {
			t.Fatalf("%q: %v %v", raw, u, err)
		}
	}
	for _, raw := range []string{"", "://x", "socks5://proxy:1080", "http://", "http://proxy?q=1", "http://proxy#f", "ftp://proxy"} {
		t.Setenv("HUB_EGRESS_PROXY", raw)
		u, err := EgressProxy()
		if raw == "" {
			if u != nil || err != nil {
				t.Fatalf("empty must mean direct: %v %v", u, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "HUB_EGRESS_PROXY") {
			t.Fatalf("%q must fail closed: %v", raw, err)
		}
	}
}

func TestEgressTransportUnsetIsDefault(t *testing.T) {
	t.Setenv("HUB_EGRESS_PROXY", "")
	if got := EgressTransport(); got != http.DefaultTransport {
		t.Fatalf("unset must keep default transport, got %T", got)
	}
}

func TestEgressTransportMalformedFailsClosed(t *testing.T) {
	t.Setenv("HUB_EGRESS_PROXY", "http://")
	resp, err := EgressTransport().RoundTrip(nil)
	if resp != nil || err == nil || !strings.Contains(err.Error(), "HUB_EGRESS_PROXY") {
		t.Fatalf("malformed proxy must fail every call: %v %v", resp, err)
	}
}

// The plain-HTTP path proves routing: the transport must speak absolute-URI
// form to the proxy and never dial the destination itself.
func TestEgressTransportRoutesThroughProxy(t *testing.T) {
	var gotURI, gotAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotAuth = r.URL.String(), r.Header.Get("Proxy-Authorization")
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("proxy-user", "proxy-pass")
	t.Setenv("HUB_EGRESS_PROXY", u.String())
	client := &http.Client{Transport: EgressTransport()}
	resp, err := client.Get("http://example.invalid/botTEST/getMe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "proxied" || gotURI != "http://example.invalid/botTEST/getMe" {
		t.Fatalf("not proxied: body=%q uri=%q", body, gotURI)
	}
	if gotAuth == "" {
		t.Fatal("proxy credentials not forwarded")
	}
}

// HTTPS egress tunnels: the proxy must see CONNECT to the target authority.
func TestEgressTransportConnectsHTTPSViaProxy(t *testing.T) {
	var gotHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("https must tunnel via CONNECT, got %s", r.Method)
			return
		}
		gotHost = r.Host
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer proxy.Close()
	t.Setenv("HUB_EGRESS_PROXY", proxy.URL)
	client := &http.Client{Transport: EgressTransport()}
	resp, err := client.Get("https://api.telegram.org/botTEST/getMe")
	if err == nil {
		resp.Body.Close()
		t.Fatal("CONNECT refusal must surface as an error")
	}
	if gotHost != "api.telegram.org:443" {
		t.Fatalf("proxy never saw CONNECT to the destination: %q", gotHost)
	}
}

// netguard.Client is the OAuth broker's transport: configured egress must
// carry it while LocalTransport — private endpoints only — stays direct.
func TestNetguardClientHonorsEgressProxy(t *testing.T) {
	var got string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxy.Close()
	t.Setenv("HUB_EGRESS_PROXY", proxy.URL)
	resp, err := Client(0, false).Get("http://example.invalid/token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "http://example.invalid/token" {
		t.Fatalf("oauth transport did not ride the proxy: %q", got)
	}
}

// With a dead proxy configured, private endpoints must still dial direct —
// the corp boundary never sees internal hops — while public transport fails.
func TestLocalTransportIgnoresEgressProxy(t *testing.T) {
	t.Setenv("HUB_EGRESS_PROXY", "http://127.0.0.1:1")
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer local.Close()
	resp, err := (&http.Client{Transport: LocalTransport()}).Get(local.URL)
	if err != nil {
		t.Fatalf("private endpoint must stay direct: %v", err)
	}
	resp.Body.Close()
	if _, err := (&http.Client{Transport: Transport(false)}).Get(local.URL); err == nil {
		t.Fatal("public transport must ride the configured proxy — dead proxy means dead call")
	}
}
