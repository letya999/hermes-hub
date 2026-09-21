package companion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"strings"
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

// RunOAuthRelay exposes only the loopback OAuth callback expected by desktop
// MCP servers. The upstream server validates the single-use state value.
func RunOAuthRelay(ctx context.Context, listen, target string) error {
	u, err := url.Parse(target)
	if listen == "" || err != nil || u.Scheme != "http" || u.Host == "" || u.Path != "/oauth2callback" || u.RawQuery != "" {
		return fmt.Errorf("OAuth relay requires an http /oauth2callback target")
	}
	targetURL := *u
	targetURL.Path = ""
	proxy := &httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(&targetURL)
		req.Out.URL.Path = "/oauth2callback"
		req.Out.URL.RawPath = ""
		req.Out.Host = targetURL.Host
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2callback" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
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

// RunOAuthExecRelay keeps authorization codes out of process arguments and
// forwards the callback into a container-local OAuth listener over stdin.
func RunOAuthExecRelay(ctx context.Context, listen, container string) error {
	if listen == "" || container == "" {
		return fmt.Errorf("OAuth exec relay listen and container are required")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/oauth2callback" || r.URL.Query().Get("state") == "" || r.URL.Query().Get("code") == "" {
			http.NotFound(w, r)
			return
		}
		cmd := exec.CommandContext(r.Context(), "docker", "exec", "-i", container, "/hermes-bridge/hubctl", "oauth-forward") // #nosec G204 -- fixed command; validated container name is operator-owned.
		cmd.Stdin = strings.NewReader(r.URL.RawQuery)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, io.Discard
		if err := cmd.Run(); err != nil {
			http.Error(w, "OAuth callback forwarding failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(output.Bytes())
	})
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

func OAuthForward(ctx context.Context, input io.Reader, output io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, 16<<10))
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("OAuth callback query is required")
	}
	values, err := url.ParseQuery(string(raw))
	if err != nil || values.Get("state") == "" || values.Get("code") == "" {
		return fmt.Errorf("invalid OAuth callback query")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:3500/oauth2callback?"+string(raw), nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(output, io.LimitReader(response.Body, 1<<20))
	return err
}
