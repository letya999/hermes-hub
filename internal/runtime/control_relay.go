package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
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
