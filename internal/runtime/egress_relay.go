package runtime

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// egressRelayHandler is the managed runtime's only reviewed path off the
// internal agent network: a forward proxy pinned to an operator-declared
// host allowlist (HUB_EGRESS_RELAY_HOSTS, comma-separated, `*.` suffix
// wildcards). It is not a generic CONNECT server — tunnels open to port 443
// on allowlisted hosts only, and plain HTTP forwarding requires an absolute
// URI whose host is listed. Everything else fails closed.
func egressRelayHandler(allow map[string]bool) http.Handler {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			host, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				host, port = r.Host, "443"
			}
			if port != "443" || !egressAllowed(allow, host) {
				http.Error(w, "host not allowed", http.StatusForbidden)
				return
			}
			upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, "443"), 10*time.Second)
			if err != nil {
				http.Error(w, "upstream unreachable", http.StatusBadGateway)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				_ = upstream.Close()
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			client, buffered, err := hijacker.Hijack()
			if err != nil {
				_ = upstream.Close()
				return
			}
			if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				_ = client.Close()
				_ = upstream.Close()
				return
			}
			if err = buffered.Flush(); err != nil {
				_ = client.Close()
				_ = upstream.Close()
				return
			}
			go func() {
				_, _ = io.Copy(upstream, client)
				_ = upstream.Close()
			}()
			go func() {
				_, _ = io.Copy(client, upstream)
				_ = client.Close()
			}()
			return
		}
		if !r.URL.IsAbs() || r.URL.Scheme != "http" || !egressAllowed(allow, r.URL.Hostname()) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		out.Header.Del("Proxy-Connection")
		out.Header.Del("Connection")
		resp, err := transport.RoundTrip(out)
		if err != nil {
			http.Error(w, "upstream unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// egressAllowed reports whether host is allowlisted: exact match or a
// `*.suffix` wildcard matching one level down (and deeper).
func egressAllowed(allow map[string]bool, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for {
		if allow[host] {
			return true
		}
		dot := strings.IndexByte(host, '.')
		if dot < 0 {
			return false
		}
		if allow["*"+host[dot:]] {
			return true
		}
		host = host[dot+1:]
	}
}

// parseEgressHosts builds the allowlist from HUB_EGRESS_RELAY_HOSTS.
// Empty or malformed entries are dropped; an empty allowlist denies
// everything, which is the safe failure shape.
func parseEgressHosts(value string) map[string]bool {
	allow := map[string]bool{}
	for _, host := range strings.Split(value, ",") {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if strings.HasPrefix(host, "*.") {
			if len(host) <= 2 || strings.ContainsAny(host[2:], ":/* ") {
				continue
			}
		} else if strings.ContainsAny(host, ":/* ") {
			continue
		}
		allow[host] = true
	}
	return allow
}

func runEgressRelay() error {
	allow := parseEgressHosts(os.Getenv("HUB_EGRESS_RELAY_HOSTS"))
	listen := strings.TrimSpace(os.Getenv("HUB_EGRESS_RELAY_LISTEN"))
	if listen == "" {
		listen = ":8319"
	}
	server := &http.Server{Addr: listen, Handler: egressRelayHandler(allow), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		return fmt.Errorf("egress-relay: %w", err)
	}
	return nil
}
