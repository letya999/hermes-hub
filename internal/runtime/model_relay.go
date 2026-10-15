package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// modelHop is one upstream in the ordered fallback ladder.
type modelHop struct {
	name   string
	base   string // fixed scheme://host[:port]; the request path appends
	keyEnv string
	mu     sync.Mutex
	cool   time.Time
}

var (
	hopNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	envKeyPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// parseModelHops reads HUB_MODEL_RELAY_UPSTREAMS: a comma list of "name=url"
// with an optional "|KEY_ENV" suffix. Unset means the single cliproxy hop.
func parseModelHops(spec string) ([]*modelHop, error) {
	if strings.TrimSpace(spec) == "" {
		spec = "cliproxy=http://cliproxy:8317"
	}
	var hops []*modelHop
	seen := map[string]bool{}
	for _, entry := range strings.Split(spec, ",") {
		name, rest, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || !hopNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid model hop %q", entry)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate model hop %q", name)
		}
		seen[name] = true
		raw, keyEnv, _ := strings.Cut(rest, "|")
		if keyEnv != "" && !envKeyPattern.MatchString(keyEnv) {
			return nil, fmt.Errorf("model hop %q key env is not an env name", name)
		}
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "..") {
			return nil, fmt.Errorf("model hop %q needs a fixed http(s) base URL", name)
		}
		hops = append(hops, &modelHop{name: name, base: strings.TrimRight(u.String(), "/"), keyEnv: keyEnv})
	}
	return hops, nil
}

func (h *modelHop) available(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cool.IsZero() || !h.cool.After(now)
}

// fail parks a hop for hopCooldown so the next request does not pay its
// timeout again. ponytail: fixed 60 s; backoff is the upgrade if needed.
const hopCooldown = time.Minute

func (h *modelHop) fail() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cool = time.Now().Add(hopCooldown)
}

func (h *modelHop) state() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]any{"name": h.name, "base": h.base}
	if !h.cool.IsZero() {
		out["cooldown_until"] = h.cool.UTC().Format(time.RFC3339)
	}
	return out
}

// retryableModelStatus marks quota/account exhaustion worth falling through to
// the next hop. Other statuses are relayed verbatim.
func retryableModelStatus(status int) bool {
	return status == 401 || status == 403 || status == 429 || status >= 500
}

// modelRelayHandler exposes the one OpenAI method needed by the pinned Hermes
// chat path plus its catalog, over an ordered fallback ladder. It never
// forwards arbitrary paths, methods, hosts or query strings, and never follows
// upstream redirects.
func modelRelayHandler(hops []*modelHop, selectedModel string) (http.Handler, error) {
	if selectedModel == "" {
		return nil, fmt.Errorf("model relay requires a selected model")
	}
	if len(hops) == 0 {
		return nil, fmt.Errorf("model relay requires at least one upstream")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport}

	call := func(ctx context.Context, method, path, auth, ctype string, body []byte) (*modelHop, *http.Response, error) {
		for _, hop := range hops {
			if !hop.available(time.Now()) {
				log.Printf("model-relay hop=%s path=%s skipped (cooldown)", hop.name, path)
				continue
			}
			req, err := http.NewRequestWithContext(ctx, method, hop.base+path, bytes.NewReader(body))
			if err != nil {
				return nil, nil, err
			}
			if ctype != "" {
				req.Header.Set("Content-Type", ctype)
			}
			if hop.keyEnv != "" {
				req.Header.Set("Authorization", "Bearer "+os.Getenv(hop.keyEnv))
			} else if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := client.Do(req)
			if err != nil {
				hop.fail()
				log.Printf("model-relay hop=%s path=%s transport error: %v", hop.name, path, err)
				continue
			}
			// Redirects are denied and quota/account exhaustion falls through;
			// both park the hop for the cooldown window.
			if (resp.StatusCode >= 300 && resp.StatusCode < 400) || retryableModelStatus(resp.StatusCode) {
				hop.fail()
				log.Printf("model-relay hop=%s path=%s status=%d", hop.name, path, resp.StatusCode)
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				continue
			}
			log.Printf("model-relay hop=%s path=%s status=%d", hop.name, path, resp.StatusCode)
			return hop, resp, nil
		}
		return nil, nil, fmt.Errorf("all model upstreams failed")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chat := r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions"
		catalog := r.Method == http.MethodGet && r.URL.Path == "/v1/models"
		hopsHealth := r.Method == http.MethodGet && r.URL.Path == "/v1/hops"
		if (!chat && !catalog && !hopsHealth) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.Scheme != "" || r.URL.Host != "" || r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		if hopsHealth {
			states := make([]map[string]any, 0, len(hops))
			for _, hop := range hops {
				states = append(states, hop.state())
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"hops": states})
			return
		}
		var body []byte
		if chat {
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
			var readErr error
			body, readErr = io.ReadAll(io.LimitReader(r.Body, maxBody+1))
			if readErr != nil {
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
		}
		hop, resp, err := call(r.Context(), r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body)
		if err != nil {
			http.Error(w, "model route unavailable", http.StatusBadGateway)
			return
		}
		if catalog && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			var catalogBody struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&catalogBody) != nil {
				http.Error(w, "model catalog unavailable", http.StatusBadGateway)
				return
			}
			filtered := catalogBody.Data[:0]
			for _, entry := range catalogBody.Data {
				if entry.ID == selectedModel {
					filtered = append(filtered, entry)
					break
				}
			}
			catalogBody.Data = filtered
			out, _ := json.Marshal(catalogBody)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Model-Hop", hop.name)
			_, _ = w.Write(out)
			return
		}
		for key, values := range resp.Header {
			w.Header()[key] = values
		}
		w.Header().Set("X-Model-Hop", hop.name)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		_ = resp.Body.Close()
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
	hops, err := parseModelHops(os.Getenv("HUB_MODEL_RELAY_UPSTREAMS"))
	if err != nil {
		return err
	}
	handler, err := modelRelayHandler(hops, os.Getenv("HUB_MODEL_RELAY_MODEL"))
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
