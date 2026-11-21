package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

// ToolHubRelayHandler exposes MCP and the caller-scoped connector inventory to the agent network.
func ToolHubRelayHandler(target string) (http.Handler, error) {
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" || upstream.RawQuery != "" || upstream.User != nil {
		return nil, fmt.Errorf("ToolHub relay requires a fixed HTTP origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(upstream)
		req.Out.URL.Path = req.In.URL.Path
		req.Out.URL.RawPath = ""
		req.Out.URL.RawQuery = ""
		req.Out.Host = upstream.Host
		for _, header := range []string{"X-HTTP-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
			req.Out.Header.Del(header)
		}
	}, ModifyResponse: func(response *http.Response) error {
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return fmt.Errorf("ToolHub upstream redirect denied")
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "ToolHub route unavailable", http.StatusBadGateway)
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcp := (r.URL.Path == "/mcp" || r.URL.Path == "/mcp-run") && (r.Method == http.MethodGet || r.Method == http.MethodPost || r.Method == http.MethodDelete)
		inventory := r.URL.Path == "/v1/connectors" && r.Method == http.MethodGet
		inbound := r.URL.Path == "/v1/inbound-fetch" && r.Method == http.MethodPost
		if (!mcp && !inventory && !inbound) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.Scheme != "" || r.URL.Host != "" || r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func runToolHubRelay(ctx context.Context) error {
	target := os.Getenv("HUB_TOOLHUB_RELAY_UPSTREAM")
	if target == "" {
		target = "http://toolhub-control:8090"
	}
	handler, err := ToolHubRelayHandler(target)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8090", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() { // #nosec G118 -- graceful shutdown needs a fresh context after ctx cancellation
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
