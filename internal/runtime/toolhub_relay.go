package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// toolHubRelayHandler exposes only the authenticated MCP endpoint to the agent network.
func toolHubRelayHandler(target string) (http.Handler, error) {
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" || upstream.RawQuery != "" || upstream.User != nil {
		return nil, fmt.Errorf("ToolHub relay requires a fixed HTTP origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(upstream)
		req.Out.URL.Path = "/mcp"
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
		if (r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete) || r.URL.Path != "/mcp" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.Scheme != "" || r.URL.Host != "" || r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func runToolHubRelay(ctx context.Context) error {
	handler, err := toolHubRelayHandler("http://toolhub-control:8090")
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8090", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
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
