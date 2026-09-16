package companion

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// RunRelay exposes only the fixed MCP endpoint to a host loopback port. It
// cannot proxy arbitrary destinations, so its bridge network is not an egress
// path for the untrusted MCP container.
func RunRelay(ctx context.Context, listen, target, token string) error {
	if listen == "" || target == "" || len(token) < 32 {
		return fmt.Errorf("relay listen, target and bearer token are required")
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.Path != "/mcp" || u.RawQuery != "" {
		return fmt.Errorf("relay target must be an http /mcp endpoint")
	}
	targetPath := u.Path
	targetURL := *u
	targetURL.Path = ""
	proxy := &httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(&targetURL)
		req.Out.URL.Path = targetPath
		req.Out.URL.RawPath = ""
		req.Out.URL.RawQuery = ""
		req.Out.Host = targetURL.Host
	},
	}
	handler := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Method == http.MethodConnect || r.Method == http.MethodTrace {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), token)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}()
	listener, err := net.Listen("tcp4", listen)
	if err != nil {
		return err
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
