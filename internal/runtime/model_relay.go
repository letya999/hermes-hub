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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// modelHop is one upstream in the ordered fallback ladder.
type modelHop struct {
	name   string
	base   string // fixed scheme://host[:port]; the request path appends
	keyEnv string
	model  string // provider-side model id; empty forwards the request model
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
	if len(hops) == 0 {
		return nil, fmt.Errorf("model relay requires at least one upstream")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		// The 3xx must reach modelCall as the hop verdict — following it would
		// let an upstream point the relay at arbitrary hosts (e.g. IMDS).
		return http.ErrUseLastResponse
	}}

	call := func(ctx context.Context, method, path, auth, ctype string, body []byte) (*modelHop, *http.Response, error) {
		return modelCall(ctx, client, hops, method, path, auth, ctype, body)
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
			if json.Unmarshal(value, &model) != nil || (selected != "" && model != selected) {
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

// Registry mode serves the reviewed relay contract from a rendered
// models.yaml instead of the static HUB_MODEL_RELAY_UPSTREAMS list.
type relayRegistryFile struct {
	Providers map[string]relayProvider `yaml:"providers"`
	Models    map[string]relayModel    `yaml:"models"`
}

type relayProvider struct {
	URL    string `yaml:"url"`
	KeyEnv string `yaml:"key_env"`
}

type relayModel struct {
	Owners []string `yaml:"owners"`
	Ladder []struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
	} `yaml:"ladder"`
}

func modelCall(ctx context.Context, client *http.Client, hops []*modelHop, method, path, auth, ctype string, body []byte) (*modelHop, *http.Response, error) {
	for _, hop := range hops {
		if !hop.available(time.Now()) {
			log.Printf("model-relay hop=%s path=%s skipped (cooldown)", hop.name, path)
			continue
		}
		hopBody := body
		if hop.model != "" {
			if remapped, err := rewriteModelField(body, hop.model); err == nil {
				hopBody = remapped
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, hop.base+path, bytes.NewReader(hopBody))
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

// rewriteModelField swaps the top-level "model" value; non-JSON bodies pass through.
func rewriteModelField(body []byte, model string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, nil
	}
	doc["model"] = model
	return json.Marshal(doc)
}

// relayRegistry owns the mounted models.yaml, the owner's current selection
// file and per-model hop ladders (so cooldown state survives re-reads).
type relayRegistry struct {
	path      string
	statePath string
	owner     string
	admin     string
	mu        sync.Mutex
	mtime     time.Time
	reg       *relayRegistryFile
	ladders   map[string][]*modelHop
}

func modelRelayRegistryHandler(registryPath, statePath, owner, admin string) (http.Handler, error) {
	r := &relayRegistry{path: registryPath, statePath: statePath, owner: owner, admin: admin, ladders: map[string][]*modelHop{}}
	if _, err := r.current(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Same review-contract fences as the env relay: fixed paths only, no
		// query strings, absolute-form targets or protocol upgrades.
		known := (req.Method == http.MethodPost && req.URL.Path == "/v1/chat/completions") ||
			(req.Method == http.MethodGet && (req.URL.Path == "/v1/models" || req.URL.Path == "/v1/hops" || req.URL.Path == "/v1/quota")) ||
			(req.Method == http.MethodPut && req.URL.Path == "/v1/model")
		if !known || req.URL.RawPath != "" || req.URL.RawQuery != "" || req.URL.Scheme != "" || req.URL.Host != "" || req.Header.Get("Upgrade") != "" {
			http.NotFound(w, req)
			return
		}
		if req.Method == http.MethodPut && req.URL.Path == "/v1/model" {
			r.serveSelect(w, req)
			return
		}
		if req.Method == http.MethodGet && req.URL.Path == "/v1/quota" {
			r.serveQuota(w, req)
			return
		}
		file, err := r.current()
		if err != nil {
			http.Error(w, "model registry unavailable", http.StatusBadGateway)
			return
		}
		if req.Method == http.MethodGet && req.URL.Path == "/v1/models" {
			names := make([]map[string]string, 0, len(file.Models))
			for name, entry := range file.Models {
				if r.granted(entry) {
					names = append(names, map[string]string{"id": name})
				}
			}
			slices.SortFunc(names, func(a, b map[string]string) int { return strings.Compare(a["id"], b["id"]) })
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": names})
			return
		}
		chat := req.Method == http.MethodPost && req.URL.Path == "/v1/chat/completions"
		hopsHealth := req.Method == http.MethodGet && req.URL.Path == "/v1/hops"
		if !chat && !hopsHealth {
			http.NotFound(w, req)
			return
		}
		var body []byte
		requested := ""
		if chat {
			mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				http.Error(w, "JSON request required", http.StatusUnsupportedMediaType)
				return
			}
			const maxBody = 32 << 20
			if req.ContentLength > maxBody {
				http.Error(w, "model request too large", http.StatusRequestEntityTooLarge)
				return
			}
			body, err = io.ReadAll(io.LimitReader(req.Body, maxBody+1))
			if err != nil || len(body) > maxBody {
				http.Error(w, "model request too large", http.StatusRequestEntityTooLarge)
				return
			}
			_ = req.Body.Close()
			if !requestsSelectedModel(body, "") {
				http.Error(w, "unapproved model", http.StatusForbidden)
				return
			}
			requested = requestedModel(body)
		}
		name, hops, err := r.hopsFor(file, requested)
		if err != nil || len(hops) == 0 {
			http.Error(w, "model not granted", http.StatusForbidden)
			return
		}
		_ = name
		if hopsHealth {
			var states []map[string]any
			for _, hop := range hops {
				states = append(states, hop.state())
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"hops": states})
			return
		}
		hop, resp, err := modelCall(req.Context(), client, hops, req.Method, req.URL.Path, req.Header.Get("Authorization"), req.Header.Get("Content-Type"), body)
		if err != nil {
			http.Error(w, "model route unavailable", http.StatusBadGateway)
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

// current returns the parsed registry, re-reading it when the file changed.
func (r *relayRegistry) current() (*relayRegistryFile, error) {
	info, err := os.Stat(r.path)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reg != nil && info.ModTime().Equal(r.mtime) {
		return r.reg, nil
	}
	b, err := os.ReadFile(r.path) // #nosec G304 -- registry path is operator-mounted
	if err != nil {
		return nil, err
	}
	var reg relayRegistryFile
	if err := yaml.Unmarshal(b, &reg); err != nil {
		return nil, err
	}
	r.reg, r.mtime, r.ladders = &reg, info.ModTime(), map[string][]*modelHop{}
	return r.reg, nil
}

func (r *relayRegistry) granted(entry relayModel) bool {
	return len(entry.Owners) == 0 || slices.Contains(entry.Owners, r.owner)
}

// hopsFor picks the logical model to serve: an explicitly granted name in the
// request, then the owner's saved selection, then "default", then the first
// granted model in sorted order.
func (r *relayRegistry) hopsFor(file *relayRegistryFile, requested string) (string, []*modelHop, error) {
	for _, name := range []string{requested, r.selection(), "default"} {
		if name == "" {
			continue
		}
		if entry, ok := file.Models[name]; ok && r.granted(entry) {
			return name, r.ladderFor(file, name, entry), nil
		}
	}
	names := make([]string, 0, len(file.Models))
	for name, entry := range file.Models {
		if r.granted(entry) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return "", nil, fmt.Errorf("no model granted to %q", r.owner)
	}
	return names[0], r.ladderFor(file, names[0], file.Models[names[0]]), nil
}

func (r *relayRegistry) ladderFor(file *relayRegistryFile, name string, entry relayModel) []*modelHop {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hops, ok := r.ladders[name]; ok {
		return hops
	}
	var hops []*modelHop
	for _, ref := range entry.Ladder {
		p := file.Providers[ref.Provider]
		base := strings.TrimRight(p.URL, "/")
		if ref.Provider == "cliproxy" && base == "" {
			base = "http://cliproxy:8317"
			if v := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_CLIPROXY")); v != "" {
				base = strings.TrimRight(v, "/")
			}
		}
		hops = append(hops, &modelHop{name: ref.Provider, base: base, keyEnv: p.KeyEnv, model: ref.Model})
	}
	r.ladders[name] = hops
	return hops
}

func (r *relayRegistry) selection() string {
	b, err := os.ReadFile(r.statePath) // #nosec G304 -- state path is operator-mounted
	if err != nil {
		return ""
	}
	var sel struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(b, &sel) != nil {
		return ""
	}
	return sel.Model
}

// serveSelect is the owner-facing model switch: admin-token authenticated,
// grant-checked and persisted into the mounted selection file.
func (r *relayRegistry) serveSelect(w http.ResponseWriter, req *http.Request) {
	if r.admin == "" || req.Header.Get("Authorization") != "Bearer "+r.admin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var sel struct {
		Model string `json:"model"`
	}
	if json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&sel) != nil || sel.Model == "" {
		http.Error(w, "invalid model request", http.StatusBadRequest)
		return
	}
	file, err := r.current()
	if err != nil {
		http.Error(w, "model registry unavailable", http.StatusBadGateway)
		return
	}
	entry, ok := file.Models[sel.Model]
	if !ok || !r.granted(entry) {
		http.Error(w, "model not granted", http.StatusForbidden)
		return
	}
	body, _ := json.Marshal(map[string]string{"model": sel.Model})
	persisted := false
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0700); err == nil {
		if tmp, err := os.CreateTemp(filepath.Dir(r.statePath), ".sel-*"); err == nil {
			_, _ = tmp.Write(body)
			if err := tmp.Close(); err == nil {
				persisted = os.Rename(tmp.Name(), r.statePath) == nil
			}
		}
	}
	if !persisted {
		http.Error(w, "selection not persisted", http.StatusBadGateway)
		return
	}
	log.Printf("model-relay owner=%s selected model=%s", r.owner, sel.Model)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// requestedModel returns the logical model name in the request body, if any.
func requestedModel(body []byte) string {
	var doc struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	return doc.Model
}

// modelRelayHandlerFromEnv picks the relay mode: a mounted models.yaml
// registry when HUB_MODEL_RELAY_REGISTRY is set, else the static env ladder.
func modelRelayHandlerFromEnv() (http.Handler, error) {
	if registry := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_REGISTRY")); registry != "" {
		state := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_STATE"))
		if state == "" {
			state = filepath.Join(filepath.Dir(registry), "model-select.json")
		}
		admin := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN"))
		if file := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN_FILE")); admin == "" && file != "" {
			if b, err := os.ReadFile(file); err == nil { // #nosec G304 -- token path is operator-mounted
				admin = strings.TrimSpace(string(b))
			}
		}
		return modelRelayRegistryHandler(registry, state, os.Getenv("HUB_MODEL_RELAY_OWNER"), admin)
	}
	hops, err := parseModelHops(os.Getenv("HUB_MODEL_RELAY_UPSTREAMS"))
	if err != nil {
		return nil, err
	}
	model := os.Getenv("HUB_MODEL_RELAY_MODEL")
	if model == "" {
		return nil, fmt.Errorf("model relay requires a selected model")
	}
	return modelRelayHandler(hops, model)
}

func runModelRelay(ctx context.Context) error {
	handler, err := modelRelayHandlerFromEnv()
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
