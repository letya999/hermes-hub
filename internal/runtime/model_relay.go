package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// modelRelayHandler exposes the one OpenAI method needed by the pinned Hermes
// chat path. It never forwards arbitrary paths, methods, hosts or query strings.
func modelRelayHandler(target, selectedModel string) (http.Handler, error) {
	if selectedModel == "" {
		return nil, fmt.Errorf("model relay requires a selected model")
	}
	upstream, err := url.Parse(target)
	if err != nil || upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" || upstream.RawQuery != "" || upstream.User != nil {
		return nil, fmt.Errorf("model relay requires a fixed HTTP origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(req *httputil.ProxyRequest) {
		req.SetURL(upstream)
		req.Out.URL.Path = "/v1/chat/completions"
		req.Out.URL.RawPath = ""
		req.Out.URL.RawQuery = ""
		req.Out.Host = upstream.Host
		for _, header := range []string{"X-HTTP-Method-Override", "X-Original-URL", "X-Rewrite-URL"} {
			req.Out.Header.Del(header)
		}
	}, ModifyResponse: func(response *http.Response) error {
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return fmt.Errorf("model upstream redirect denied")
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "model route unavailable", http.StatusBadGateway)
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.Scheme != "" || r.URL.Host != "" || r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "JSON request required", http.StatusUnsupportedMediaType)
			return
		}
		const maxBody = 32 << 20
		if r.ContentLength > maxBody {
			http.Error(w, "model request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body == nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		if len(body) > maxBody {
			http.Error(w, "model request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if !requestsSelectedModel(body, selectedModel) {
			http.Error(w, "unapproved model", http.StatusForbidden)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		proxy.ServeHTTP(w, r)
	}), nil
}

func requestsSelectedModel(body []byte, selected string) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	count := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		if strings.EqualFold(name, "model") {
			if name != "model" {
				return false
			}
			count++
			var model string
			if json.Unmarshal(value, &model) != nil || model != selected {
				return false
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return false
	}
	var trailing any
	return count == 1 && decoder.Decode(&trailing) == io.EOF
}

func runModelRelay(ctx context.Context) error {
	handler, err := modelRelayHandler("http://cliproxy:8317", os.Getenv("HUB_MODEL_RELAY_MODEL"))
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8318", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
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
