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

// controlRelayHandler exposes the managed runtime's control API to the host
// supervisor through a fixed reviewed upstream. The managed runtime sits on an
// internal-only agent network, so this dual-homed relay is the only inbound
// path; it is the supervisor-facing counterpart of the agent's narrow egress.
func controlRelayHandler(target string) (http.Handler, error) {
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" || upstream.RawQuery != "" || upstream.User != nil {
		return nil, fmt.Errorf("control relay requires a fixed HTTP origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(upstream)
		req.Out.Host = upstream.Host
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
		proxy.ServeHTTP(w, r)
	}), nil
}

func runControlRelay(ctx context.Context) error {
	handler, err := controlRelayHandler(os.Getenv("HUB_CONTROL_RELAY_TARGET"))
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8091", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
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
