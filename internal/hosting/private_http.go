package hosting

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var privateHost = regexp.MustCompile(`^hosting-[a-z][a-z0-9-]{0,39}-(dev|prod)(-ctl)?$`)
var privateTransport = &http.Transport{
	Proxy:        nil,
	DialContext:  (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 8,
	IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 28 * time.Second,
	MaxResponseHeaderBytes: 32 << 10,
}

// PrivateEndpoint admits only loopback or the generated internal service names.
// The caller supplies the role's fixed port/path, never tool arguments.
func PrivateEndpoint(raw, port, path string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != path && u.Path != "" {
		return false
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return false
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return ip.IsLoopback()
	}
	return u.Port() == port && privateHost.MatchString(u.Hostname()) && u.Path == path
}

// PrivateClient keeps service credentials off ambient HTTP proxies/cookie jars
// and refuses redirects. The connection pool and response headers are bounded.
func PrivateClient(base *http.Client) *http.Client {
	client := http.Client{Timeout: 28 * time.Second}
	if base != nil && base.Timeout > 0 && base.Timeout < client.Timeout {
		client.Timeout = base.Timeout
	}
	client.Transport = privateTransport
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
