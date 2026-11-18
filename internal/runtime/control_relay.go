package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The managed runtime's API server accepts a self-asserted `hosted_room_dispatch`
// plus `_room_execution_policy` in a /v1/runs body (the upstream digest is a
// self-computable hash, not a signature). On that path Hermes replaces the
// enabled toolset list and never subtracts the managed denylist, so a forged
// body arms arbitrary tools with approvals off. This relay is the only inbound
// path to a managed runtime; denying the keys here is the control-plane fix.
var controlDeniedBodyKeys = []string{"hosted_room_dispatch", "_room_execution_policy"}

// Bodies beyond this bound cannot be proven free of denied keys.
const controlBodyInspectLimit = 8 << 20

func controlBodyDenied(r *http.Request) (denied bool, err error) {
	if r.Body == nil || r.ContentLength == 0 {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, controlBodyInspectLimit+1))
	if err != nil {
		return false, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > controlBodyInspectLimit {
		return true, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return false, nil // Not a JSON object; upstream rejects it on its own.
	}
	for _, key := range controlDeniedBodyKeys {
		if _, ok := top[key]; ok {
			return true, nil
		}
	}
	return false, nil
}

// controlRelayHandler exposes the managed runtime's control API to the host
// supervisor through a fixed reviewed upstream. The managed runtime sits on an
// internal-only agent network, so this dual-homed relay is the only inbound
// path; it is the supervisor-facing counterpart of the agent's narrow egress.
// vsockDial bridges the firecracker vsock UDS into a TCP-like stream for the
// HTTP transport: dial the unix socket, then send "CONNECT <guestPort>\n" per
// the firecracker vsock handshake. Used by the firecracker backend where the
// control relay container mounts the box workdir and has no TCP route to the
// guest.
func vsockDialContext(udsPath, guestPort string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", udsPath)
		if err != nil {
			return nil, err
		}
		if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			c.Close() // #nosec G104 -- best-effort; error intentionally ignored
			return nil, err
		}
		if _, err := fmt.Fprintf(c, "CONNECT %s\n", guestPort); err != nil {
			c.Close() // #nosec G104 -- best-effort; error intentionally ignored
			return nil, err
		}
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "OK") {
			c.Close() // #nosec G104 -- best-effort; error intentionally ignored
			return nil, fmt.Errorf("vsock connect refused: %s", strings.TrimSpace(line))
		}
		// The buffered reader may already hold proxied bytes; wrap it so the
		// transport sees a clean stream.
		if err := c.SetDeadline(time.Time{}); err != nil {
			c.Close() // #nosec G104 -- best-effort; error intentionally ignored
			return nil, err
		}
		return &vsockPrefixConn{Conn: c, r: bufio.NewReader(c)}, nil
	}
}

type vsockPrefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *vsockPrefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func controlRelayHandler(target string) (http.Handler, error) {
	upstream, err := url.Parse(target)
	if err != nil || upstream.User != nil || upstream.RawQuery != "" {
		return nil, fmt.Errorf("control relay requires a fixed HTTP origin")
	}
	var vsockUDS string
	proxyURL := upstream
	if upstream.Scheme == "vsock" {
		port, perr := strconv.Atoi(upstream.Host)
		if perr != nil || port <= 0 || port > 65535 || !strings.HasPrefix(upstream.Path, "/") || !strings.HasSuffix(upstream.Path, ".sock") || strings.Contains(upstream.Path, "..") {
			return nil, fmt.Errorf("control relay vsock target must be vsock://<guest-port>/<abs uds path>")
		}
		vsockUDS = upstream.Path
		// The HTTP transport only speaks http(s); the dialer ignores this
		// authority and connects through the vsock UDS instead.
		proxyURL = &url.URL{Scheme: "http", Host: "vsock.local"}
	} else if upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" {
		return nil, fmt.Errorf("control relay requires a fixed HTTP origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if vsockUDS != "" {
		transport.DialContext = vsockDialContext(vsockUDS, upstream.Host)
		transport.DisableKeepAlives = true
	}
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(proxyURL)
		req.Out.Host = proxyURL.Host
		for _, header := range []string{"X-HTTP-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
			req.Out.Header.Del(header)
		}
	}, ModifyResponse: func(response *http.Response) error {
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return fmt.Errorf("control upstream redirect denied")
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "runtime control route unavailable", http.StatusBadGateway)
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Scheme != "" || r.URL.Host != "" || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		denied, err := controlBodyDenied(r)
		if err != nil {
			http.Error(w, "runtime control body unreadable", http.StatusBadRequest)
			return
		}
		if denied {
			http.Error(w, "runtime control body denied", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func runControlRelay(ctx context.Context) error {
	handler, err := controlRelayHandler(os.Getenv("HUB_CONTROL_RELAY_TARGET"))
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8091", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { // #nosec G118 -- graceful shutdown needs a fresh context after ctx cancellation
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(stop) // #nosec G104 -- best-effort; error intentionally ignored
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
