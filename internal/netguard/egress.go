package netguard

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// EgressProxy parses HUB_EGRESS_PROXY — the operator-configured corporate
// HTTP proxy the hub's own outbound clients (Telegram polling, Slack API and
// Socket Mode, OAuth token/discovery calls) must use. Empty means direct
// egress; a malformed value is an error so a half-set deployment fails
// closed instead of silently bypassing the perimeter. URL userinfo stays
// legal: corporate forward proxies commonly authenticate via basic auth.
func EgressProxy() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("HUB_EGRESS_PROXY"))
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("HUB_EGRESS_PROXY must be an http(s) proxy URL")
	}
	return u, nil
}

// EgressTransport returns the outbound transport for the hub's channel
// clients: when HUB_EGRESS_PROXY names a proxy every request CONNECTs
// through it — the destination allowlist is enforced host-side, so the
// explicit configuration beats ambient HTTP(S)_PROXY variables. Unset keeps
// the default transport semantics, including env-proxy handling; a malformed
// value yields a transport that fails every call — the floor under startup
// validation.
func EgressTransport() http.RoundTripper {
	proxy, err := EgressProxy()
	if err != nil {
		return errTransport{err}
	}
	if proxy == nil {
		return http.DefaultTransport
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// A replaced DefaultTransport owns the wire itself; mirror the
		// transport() fallback and leave it alone.
		return http.DefaultTransport
	}
	clone := base.Clone()
	clone.Proxy = http.ProxyURL(proxy)
	return clone
}

// errTransport fails every call with the configuration error — a malformed
// HUB_EGRESS_PROXY can never degrade into silent direct egress.
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
