package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// egressRelayHandler is the managed runtime's only reviewed path off the
// internal agent network: a forward proxy pinned to an operator-declared
// host allowlist (HUB_EGRESS_RELAY_HOSTS, comma-separated, `*.` suffix
// wildcards). CONNECT opens port 443 on listed hosts, plus the single exact
// sish relay address rendered from reviewed exposure config. Plain HTTP still
// requires an absolute URI whose host is listed. Everything else fails closed.
// egressRelayResponseTimeout bounds how long the relay waits for upstream
// response headers. Control-plane calls legitimately span minutes — a
// skill-git install is a sequence of bounded 60s cells — so the cap sits
// above that budget; the caller's own context still ends each request early.
var egressRelayResponseTimeout = 10 * time.Minute

func egressRelayHandler(allow map[string]bool, connectTarget string) http.Handler {
	return signedEgressRelayHandler(allow, connectTarget, "", nil)
}

func signedEgressRelayHandler(allow map[string]bool, connectTarget, owner string, authority *relayRunAuthority) http.Handler {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: egressRelayResponseTimeout,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protected := authority != nil || r.Header.Get(identity.EgressRunTokenHeader) != ""
		if protected {
			policy, err := authority.authorize(r, identity.EgressAudience, owner)
			read := r.Method == http.MethodGet || r.Method == http.MethodHead
			if err != nil || (!read && !policy.Identity.Restrictions.AllowsUnattendedWrite()) {
				log.Printf("egress-relay owner=%s outcome=deny reason=run-authorization", owner)
				lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
				http.Error(w, locale.T(lang, "run.authorization-denied"), http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodConnect {
			host, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				host, port = r.Host, "443"
			}
			if !(port == "443" && egressAllowed(allow, host)) && (connectTarget == "" || !strings.EqualFold(net.JoinHostPort(host, port), connectTarget)) {
				http.Error(w, "host not allowed", http.StatusForbidden)
				return
			}
			upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second) // #nosec G704 -- host:port is allowlisted above
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
			tunnel, closeTunnel := context.WithCancel(context.WithoutCancel(r.Context()))
			if protected {
				go func() {
					ticker := time.NewTicker(2 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-tunnel.Done():
							return
						case <-ticker.C:
							verification, cancel := context.WithTimeout(tunnel, 2*time.Second)
							_, err := authority.authorize(r.WithContext(verification), identity.EgressAudience, owner)
							cancel()
							if err != nil {
								_ = client.Close()
								_ = upstream.Close()
								return
							}
						}
					}
				}()
			}
			go func() {
				defer closeTunnel()
				_, _ = io.Copy(upstream, client)
				_ = upstream.Close()
			}()
			go func() {
				defer closeTunnel()
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
		out.Header.Del(identity.EgressRunTokenHeader)
		out.Header.Del(identity.MemoryRunTokenHeader)
		out.Header.Del(identity.CommunicationRunTokenHeader)
		out.Header.Del(identity.ModelServiceTokenHeader)
		out.Header.Del(RunDeliveryHeader)
		out.Header.Del(api.HeaderJobID)
		if r.URL.Host != "communication-hub:8081" {
			out.Header.Del(identity.RunTokenHeader)
			out.Header.Del(api.HeaderRunID)
		}
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
	connectTarget := strings.TrimSpace(os.Getenv("HUB_EGRESS_RELAY_CONNECT_TARGET"))
	listen := strings.TrimSpace(os.Getenv("HUB_EGRESS_RELAY_LISTEN"))
	if listen == "" {
		listen = ":8319"
	}
	handler := signedEgressRelayHandler(allow, connectTarget, os.Getenv("HUB_EGRESS_RELAY_OWNER"), relayRunAuthorityFromEnv())
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		return fmt.Errorf("egress-relay: %w", err)
	}
	return nil
}
