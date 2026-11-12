package runtime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/netguard"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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

// modelHop is one upstream in the ordered fallback ladder. Each hop is one
// credential principal (one service); models is the ordered in-service model
// list retried on model-level failures before the ladder advances.
type modelHop struct {
	*modelHealth
	keyFor    func(context.Context) (string, error)
	transport http.RoundTripper
	name      string
	base      string // fixed scheme://host[:port]; the request path appends
	keyEnv    string
	model     string // provider-side model id; empty forwards the request model
	models    []string
	limits    []stack.ModelLimit
	quota     []stack.ModelLimit // measurable provider_quota windows
	disabled  string             // non-empty: service parked, reason for logs
	reasoning bool
	meter     *relayMeter
	owner     string
}

type modelHealth struct {
	mu          sync.Mutex
	cool        time.Time
	recoverable bool
	retrying    bool
	lastClass   string // quota | provider_block | transport | other
	lastDetail  string
}

// down reports whether the provider is still inside its cooldown window.
func (h *modelHealth) down(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.cool.IsZero() && h.cool.After(now)
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
	if len(strings.Split(spec, ",")) > 8 {
		return nil, fmt.Errorf("model relay supports at most eight hops")
	}
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
		hops = append(hops, &modelHop{modelHealth: &modelHealth{}, name: name, base: strings.TrimRight(u.String(), "/"), keyEnv: keyEnv})
	}
	return hops, nil
}

func (h *modelHop) availableFor(now time.Time, reserved bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.disabled != "" {
		return false
	}
	return (!h.retrying || reserved) && (h.cool.IsZero() || !h.cool.After(now))
}

// fail parks a hop for hopCooldown so the next request does not pay its
// timeout again. ponytail: fixed 60 s; backoff is the upgrade if needed.
const hopCooldown = time.Minute

func (h *modelHop) fail() { h.failWith("other", "") }

func (h *modelHop) failWith(class, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cool = time.Now().Add(hopCooldown)
	h.recoverable = false
	h.lastClass, h.lastDetail = class, detail
}

func (h *modelHop) failTransport() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cool = time.Now().Add(hopCooldown)
	h.recoverable = true
	h.lastClass, h.lastDetail = "transport", ""
}

// failure reports the classified reason the hop last failed, for operator
// surfaces and the ladder's typed error.
func (h *modelHealth) failure() (class, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastClass, h.lastDetail
}

// waitForModelRecovery reserves one bounded probe after a transport timeout.
// Other callers keep the hop's cooldown instead of stampeding the provider.
func waitForModelRecovery(ctx context.Context, hops []*modelHop) (*modelHop, func()) {
	now := time.Now()
	for _, hop := range hops {
		hop.mu.Lock()
		healthy := !hop.recoverable && !hop.retrying && (hop.cool.IsZero() || !hop.cool.After(now))
		hop.mu.Unlock()
		if healthy {
			return nil, func() {}
		}
	}
	for _, hop := range hops {
		hop.mu.Lock()
		if !hop.recoverable || hop.retrying {
			hop.mu.Unlock()
			continue
		}
		delay := time.Until(hop.cool)
		hop.retrying = true
		hop.mu.Unlock()
		timer := time.NewTimer(max(0, delay))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
		return hop, func() { hop.mu.Lock(); hop.retrying = false; hop.mu.Unlock() }
	}
	return nil, func() {}
}

func (h *modelHop) state() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]any{"name": h.name, "base": h.base}
	if !h.cool.IsZero() {
		out["cooldown_until"] = h.cool.UTC().Format(time.RFC3339)
	}
	if h.lastClass != "" {
		out["failure"] = h.lastClass
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
	hopTimeout, total, err := modelRelayLimits()
	if err != nil {
		return nil, err
	}
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
		return modelCall(ctx, client, hops, method, path, auth, ctype, body, hopTimeout)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), total)
		defer cancel()
		r = r.WithContext(ctx)
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
			writeModelFailure(w, err)
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
		copyModelBody(w, resp)
		_ = resp.Body.Close()
	}), nil
}

func writeModelFailure(w http.ResponseWriter, err error) {
	code, status, message := "model_route_unavailable", http.StatusBadGateway, "model route unavailable"
	var typed *modelRouteError
	if errors.As(err, &typed) {
		switch typed.class {
		case "quota":
			code, status, message = "model_quota_exhausted", http.StatusTooManyRequests, "model quota exhausted; no configured fallback remains"
		case "provider_block":
			code, message = "model_provider_blocked", "model blocked by provider"
		}
		if typed.detail != "" {
			message += ": " + typed.detail
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	out, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	_, _ = w.Write(out)
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
type relayRegistryFile = stack.ModelRegistry
type relayModel = stack.ModelEntry

func modelCall(ctx context.Context, client *http.Client, hops []*modelHop, method, path, auth, ctype string, body []byte, hopTimeout time.Duration) (*modelHop, *http.Response, error) {
	bounded := *client
	recovering, releaseRecovery := waitForModelRecovery(ctx, hops)
	defer releaseRecovery()
	for i, hop := range hops {
		started := time.Now()
		if !hop.availableFor(time.Now(), hop == recovering) {
			log.Printf("model-relay hop=%s path=%s skipped (cooldown)", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
			continue
		}
		if !hop.reasoning && requestNeedsReasoning(body) {
			log.Printf("model-relay hop=%s path=%s skipped (reasoning unsupported)", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
			continue
		}
		if hop.meter != nil && hop.meter.exhausted(hop.name, hop.limits, hop.owner) {
			hop.failWith("quota", "local model limit spent")
			log.Printf("model-relay hop=%s path=%s skipped (quota)", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
			continue
		}
		if len(hop.quota) > 0 {
			exhausted, err := hop.providerQuotaExhausted(ctx)
			if err != nil {
				hop.fail()
				log.Printf("model-relay hop=%s path=%s skipped (quota unread)", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				continue
			}
			if exhausted {
				hop.failWith("quota", "provider quota window spent")
				log.Printf("model-relay hop=%s path=%s skipped (provider_quota)", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				continue
			}
		}
		hopBody := body
		if len(hop.models) > 0 {
			if remapped, err := rewriteModelField(body, hop.models[0]); err == nil {
				hopBody = remapped
			}
		} else if hop.model != "" {
			if remapped, err := rewriteModelField(body, hop.model); err == nil {
				hopBody = remapped
			}
		}
		// Runaway ceiling only: well above the managed compaction trigger so
		// in-loop compression stays the primary bound.
		if method == http.MethodPost && path == "/v1/chat/completions" && !withinModelBudget(hopBody) {
			return hop, &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"context_budget_exceeded","message":"request exceeds conservative token ceiling"}}`))}, nil
		}
		target := hop.base + path
		if base, err := url.Parse(hop.base); err == nil && strings.Trim(base.Path, "/") != "" {
			target = hop.base + strings.TrimPrefix(path, "/v1")
		}
		credential := auth
		if hop.keyFor != nil {
			credentialContext, cancel := context.WithTimeout(ctx, hopTimeout)
			key, err := hop.keyFor(credentialContext)
			cancel()
			if err != nil {
				hop.fail()
				log.Printf("model-relay hop=%s path=%s credential unavailable", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				continue
			}
			credential = "Bearer " + key
		} else if hop.keyEnv != "" {
			key := os.Getenv(hop.keyEnv)
			if key == "" {
				hop.fail()
				log.Printf("model-relay hop=%s path=%s credential unavailable", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				continue
			}
			credential = "Bearer " + key
		}
		bounded.Transport = client.Transport
		if hop.transport != nil {
			bounded.Transport = hop.transport
		}
		// Model-level failures (the account does not serve this model) retry
		// the next model on the same principal; quota/auth/transport failures
		// park the whole service and advance the ladder.
		for attempt := 0; ; attempt++ {
			if attempt > 0 && attempt < len(hop.models) {
				if remapped, err := rewriteModelField(body, hop.models[attempt]); err == nil {
					hopBody = remapped
				}
			}
			req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(hopBody)) // #nosec G704 -- configured upstream hop
			if err != nil {
				return nil, nil, err
			}
			if ctype != "" {
				req.Header.Set("Content-Type", ctype)
			}
			if credential != "" {
				req.Header.Set("Authorization", credential)
			}
			bounded.Timeout = hopTimeout - time.Since(started)
			if bounded.Timeout <= 0 {
				hop.fail()
				break
			}
			resp, err := bounded.Do(req) // #nosec G704 -- configured upstream hop
			if err != nil {
				hop.failTransport()
				log.Printf("model-relay hop=%s path=%s transport error", hop.name, path) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				break
			}
			if hop.meter != nil {
				hop.meter.charge(hop.name, hop.limits, hop.owner, hopBody)
			}
			// Redirects are denied and quota/account exhaustion falls through;
			// both park the hop for the cooldown window.
			if (resp.StatusCode >= 300 && resp.StatusCode < 400) || retryableModelStatus(resp.StatusCode) {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				_ = resp.Body.Close()
				class, detail := classifyUpstreamError(resp.StatusCode, raw)
				hop.failWith(class, detail)
				log.Printf("model-relay hop=%s path=%s status=%d class=%s", hop.name, path, resp.StatusCode, class) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				break
			}
			if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
				if attempt+1 < len(hop.models) {
					log.Printf("model-relay hop=%s path=%s model=%s status=%d", hop.name, path, hop.models[attempt], resp.StatusCode) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					_ = resp.Body.Close()
					continue
				}
				if len(hop.models) > 0 {
					// Every declared model refused — the service does not
					// serve this request shape; the ladder advances without
					// parking the hop (the verdict is request-specific).
					log.Printf("model-relay hop=%s path=%s model=%s status=%d", hop.name, path, hop.models[attempt], resp.StatusCode) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					_ = resp.Body.Close()
					break
				}
			}
			hop.mu.Lock()
			hop.cool, hop.recoverable = time.Time{}, false
			hop.lastClass, hop.lastDetail = "", ""
			hop.mu.Unlock()
			log.Printf("model-relay hop=%s path=%s status=%d", hop.name, path, resp.StatusCode) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
			if i > 0 {
				primary := hops[0]
				class, detail := primary.failure()
				if class == "" {
					class = "unavailable"
				}
				log.Printf("model-relay fallback from=%s class=%s detail=%q served=%s", primary.name, class, detail, hop.name) // #nosec G706 -- diagnostics log on the internal relay; hop/path are operator scope
				resp.Header.Set("X-Model-Fallback", primary.name)
				resp.Header.Set("X-Model-Fallback-Reason", class)
			}
			return hop, resp, nil
		}
	}
	return nil, nil, classifyLadderFailure(hops)
}

// modelRouteError carries the classified reason the whole ladder refused so
// the response can name it: a quota wall with no remaining fallback or a
// generic outage.
type modelRouteError struct {
	class  string
	detail string
}

func (e *modelRouteError) Error() string {
	if e.detail != "" {
		return e.class + ": " + e.detail
	}
	return e.class
}

// classifyLadderFailure reduces the per-hop failure set to the dominant
// class. A provider block outranks: it needs an operator decision, not a
// retry. An all-quota ladder is "exhausted with nothing to switch to";
// anything else stays the generic route failure.
func classifyLadderFailure(hops []*modelHop) *modelRouteError {
	saw, blocked, allQuota := false, false, true
	blockDetail, quotaDetail := "", ""
	for _, hop := range hops {
		class, d := hop.failure()
		if class == "" {
			continue
		}
		saw = true
		switch class {
		case "provider_block":
			blocked = true
			if blockDetail == "" {
				blockDetail = d
			}
		case "quota":
			if quotaDetail == "" {
				quotaDetail = d
			}
		default:
			allQuota = false
		}
	}
	switch {
	case blocked:
		return &modelRouteError{class: "provider_block", detail: blockDetail}
	case saw && allQuota:
		return &modelRouteError{class: "quota", detail: quotaDetail}
	default:
		return &modelRouteError{class: "route_unavailable"}
	}
}

// classifyUpstreamError buckets an upstream refusal into the failure class
// the ladder reports: rate limits and account-level provider blocks (ToS,
// suspension, disabled service) get named classes; detail carries the
// provider's own message for logs and error results.
func classifyUpstreamError(status int, raw []byte) (class, detail string) {
	var doc struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &doc)
	detail = doc.Error.Message
	if status == http.StatusTooManyRequests {
		return "quota", detail
	}
	if status == http.StatusForbidden {
		hay := strings.ToLower(doc.Error.Status + " " + doc.Error.Message)
		for _, mark := range []string{"permission_denied", "terms of service", "violation", "disabled", "suspended", "deactivated", "blocked", "banned"} {
			if strings.Contains(hay, mark) {
				return "provider_block", detail
			}
		}
	}
	return "other", detail
}

// modelTokenCeiling refuses requests no provider window could serve. It sits
// above compression.threshold_tokens (200000) so Hermes in-loop compaction
// handles long sessions first and the gate only fires on runaway growth.
const modelTokenCeiling = 262144

func withinModelBudget(body []byte) bool {
	var output struct {
		MaxTokens           int64 `json:"max_tokens"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
		Messages            []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &output) != nil {
		return false
	}
	reserve := max(int64(8192), output.MaxTokens, output.MaxCompletionTokens)
	if reserve > modelTokenCeiling {
		return false
	}
	// Data URLs are image payloads, not prompt text. Charge a conservative
	// image allowance while the HTTP byte limit still caps their encoded size.
	imageBytes, images := 0, 0
	for _, message := range output.Messages {
		var parts []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if part.Type != "image_url" {
				continue
			}
			url := part.ImageURL.URL
			comma := strings.IndexByte(url, ',')
			if comma < 0 || comma > 128 || !strings.HasPrefix(url, "data:image/") || !strings.HasSuffix(url[:comma], ";base64") {
				continue
			}
			imageBytes += len(url)
			images++
		}
	}
	// Worst case ~6 UTF-8/JSON bytes per text token (escapes, CJK).
	return int64(len(body)-imageBytes)/6+reserve+int64(images)*16384 <= modelTokenCeiling
}

// rewriteModelField swaps the top-level "model" value; non-JSON bodies pass through.
func rewriteModelField(body []byte, model string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, nil
	}
	doc["model"], _ = json.Marshal(model)
	return json.Marshal(doc)
}

type modelStreamWriter struct{ http.ResponseWriter }

func (w modelStreamWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}
func copyModelBody(w http.ResponseWriter, response *http.Response) {
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		_, _ = io.Copy(modelStreamWriter{w}, response.Body)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// relayRegistry owns the mounted models.yaml, the owner's current selection
// file and per-model hop ladders (so cooldown state survives re-reads).
type relayRegistry struct {
	broker    credentialbroker.Config
	path      string
	statePath string
	owner     string
	admin     string
	auth      string
	decider   api.DecideClient
	mu        sync.Mutex
	mtime     time.Time
	reg       *relayRegistryFile
	ladders   map[string][]*modelHop
	health    map[string]*modelHealth
	meter     *relayMeter
}

func modelRelayRegistryHandler(registryPath, statePath, owner, admin string) (http.Handler, error) {
	hopTimeout, total, err := modelRelayLimits()
	if err != nil {
		return nil, err
	}
	r := &relayRegistry{path: registryPath, statePath: statePath, owner: owner, admin: admin, auth: os.Getenv("HUB_MODEL_RELAY_AUTH"), ladders: map[string][]*modelHop{},
		decider: api.DecideClient{URL: strings.TrimSpace(os.Getenv("HUB_TOOLHUB_URL")), Token: strings.TrimSpace(os.Getenv("HUB_TOOLHUB_CONTROL")), Principal: owner, Environment: strings.TrimSpace(os.Getenv("HUB_ENVIRONMENT"))}}
	brokerConfig, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_MODEL_")
	if err != nil {
		return nil, err
	}
	r.broker = brokerConfig
	if _, err := r.current(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), total)
		defer cancel()
		req = req.WithContext(ctx)
		// Same review-contract fences as the env relay: fixed paths only, no
		// query strings, absolute-form targets or protocol upgrades.
		known := (req.Method == http.MethodPost && req.URL.Path == "/v1/chat/completions") ||
			(req.Method == http.MethodGet && (req.URL.Path == "/v1/models" || req.URL.Path == "/v1/hops" || req.URL.Path == "/v1/quota" || req.URL.Path == "/v1/model" || req.URL.Path == "/v1/registry")) ||
			(req.Method == http.MethodPut && req.URL.Path == "/v1/model")
		if !known || req.URL.RawPath != "" || req.URL.RawQuery != "" || req.URL.Scheme != "" || req.URL.Host != "" || req.Header.Get("Upgrade") != "" {
			http.NotFound(w, req)
			return
		}
		if r.auth != "" && req.Method == http.MethodGet && (req.URL.Path == "/v1/models" || req.URL.Path == "/v1/hops") {
			authorized := subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+r.auth)) == 1
			if !authorized && r.admin != "" {
				authorized = subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+r.admin)) == 1
			}
			if !authorized {
				log.Printf("model-relay owner=%s outcome=deny reason=metadata-caller", r.owner)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if req.URL.Path == "/v1/model" {
			r.serveSelect(w, req)
			return
		}
		if req.Method == http.MethodGet && (req.URL.Path == "/v1/models" || req.URL.Path == "/v1/hops") && !r.grantsRight(req.Context(), "list", "") {
			http.Error(w, "right denied", http.StatusForbidden)
			return
		}
		if req.Method == http.MethodGet && req.URL.Path == "/v1/registry" {
			r.serveRegistry(w, req)
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
				if r.granted(file, entry) {
					names = append(names, map[string]string{"id": name})
				}
			}
			slices.SortFunc(names, func(a, b map[string]string) int { return strings.Compare(a["id"], b["id"]) })
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": names})
			return
		}
		chat := req.Method == http.MethodPost && req.URL.Path == "/v1/chat/completions"
		if chat && r.auth != "" && subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+r.auth)) != 1 {
			log.Printf("model-relay owner=%s outcome=deny reason=caller", r.owner)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
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
			if !r.grantsRight(req.Context(), "use", requested) {
				log.Printf("model-relay owner=%s outcome=deny reason=rights models.use", r.owner)
				http.Error(w, "right denied", http.StatusForbidden)
				return
			}
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
		providerAuth := req.Header.Get("Authorization")
		if r.auth != "" {
			providerAuth = ""
		}
		hop, resp, err := modelCall(req.Context(), client, hops, req.Method, req.URL.Path, providerAuth, req.Header.Get("Content-Type"), body, hopTimeout)
		if err != nil {
			writeModelFailure(w, err)
			return
		}
		for key, values := range resp.Header {
			w.Header()[key] = values
		}
		w.Header().Set("X-Model-Hop", hop.name)
		w.WriteHeader(resp.StatusCode)
		copyModelBody(w, resp)
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
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err := decoder.Decode(&reg); err != nil {
		return nil, fmt.Errorf("invalid model registry")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid model registry trailing document")
	}
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	r.reg, r.mtime, r.ladders = &reg, info.ModTime(), map[string][]*modelHop{}
	r.health = map[string]*modelHealth{}
	return r.reg, nil
}

func (r *relayRegistry) granted(file *relayRegistryFile, entry relayModel) bool {
	if !entry.Allows(r.owner) {
		return false
	}
	for _, hop := range entry.Ladder {
		provider := file.Providers[hop.Provider]
		if !provider.Allows(r.owner) {
			return false
		}
	}
	return true
}

// grantedProviders returns the accounts the owner can reach: providers wired
// into at least one granted model's ladder. Ad-hoc catalog selections are
// limited to this set — a provider that exists in the file but not on a
// granted ladder stays unreachable.
func (r *relayRegistry) grantedProviders(file *relayRegistryFile) map[string]bool {
	set := map[string]bool{}
	for _, entry := range file.Models {
		if !r.granted(file, entry) {
			continue
		}
		for _, ref := range entry.Ladder {
			set[ref.Provider] = true
		}
	}
	return set
}

// switchLevel resolves the owner's switch grant: switch[owner], then the "*"
// fallback, then "logical" — the long-standing default where only granted
// registry names are selectable.
func (r *relayRegistry) switchLevel(file *relayRegistryFile) string {
	if level, ok := file.Switch[r.owner]; ok {
		return level
	}
	if level, ok := file.Switch["*"]; ok {
		return level
	}
	return "logical"
}

// catalogProvider finds which granted account actually serves a model id. A
// "provider/model" argument pins the account; a bare id resolves against each
// granted provider's live catalog in sorted order. Empty when nothing serves
// it — the caller then denies the switch.
func (r *relayRegistry) catalogProvider(ctx context.Context, file *relayRegistryFile, name string) string {
	hopsByProvider, providers := r.providerHops(file)
	if provider, model, ok := strings.Cut(name, "/"); ok {
		hop := hopsByProvider[provider]
		if hop == nil {
			return ""
		}
		if slices.Contains(r.providerCatalog(ctx, hop), model) {
			return provider
		}
		return ""
	}
	for _, provider := range providers {
		if hop := hopsByProvider[provider]; hop != nil && slices.Contains(r.providerCatalog(ctx, hop), name) {
			return provider
		}
	}
	return ""
}

// providerHops returns one live hop per granted provider (sorted names) so a
// caller can query each account's upstream catalog.
func (r *relayRegistry) providerHops(file *relayRegistryFile) (map[string]*modelHop, []string) {
	hopsByProvider := map[string]*modelHop{}
	var providers []string
	names := make([]string, 0, len(file.Models))
	for name, entry := range file.Models {
		if r.granted(file, entry) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		hops := r.ladderFor(file, name, file.Models[name])
		for i, ref := range file.Models[name].Ladder {
			if _, seen := hopsByProvider[ref.Provider]; !seen && i < len(hops) && hops[i].name == ref.Provider {
				hopsByProvider[ref.Provider] = hops[i]
				providers = append(providers, ref.Provider)
			}
		}
	}
	slices.Sort(providers)
	return hopsByProvider, providers
}

// hopsFor picks the logical model to serve: an explicitly granted name in the
// request, then the owner's saved selection, then "default", then the first
// granted model in sorted order.
func (r *relayRegistry) hopsFor(file *relayRegistryFile, requested string) (string, []*modelHop, error) {
	if requested == file.RuntimeModel {
		requested = ""
	}
	if r.auth != "" && requested != "" {
		entry, ok := file.Models[requested]
		if !ok || !r.granted(file, entry) {
			log.Printf("model-relay owner=%s outcome=deny reason=model", r.owner)
			return "", nil, fmt.Errorf("model not granted")
		}
	}
	sel := r.selection()
	for _, name := range []string{requested, sel.Model, "default"} {
		if name == "" {
			continue
		}
		if entry, ok := file.Models[name]; ok && r.granted(file, entry) {
			return name, r.ladderFor(file, name, entry), nil
		}
		// An ad-hoc catalog selection resolves to a single-hop ladder on the
		// account it was persisted for — still validated against the live
		// registry and the current switch grant so a revoked provider or a
		// downgraded grant can never serve it.
		if name == sel.Model && sel.Provider != "" && r.switchLevel(file) == "catalog" && r.grantedProviders(file)[sel.Provider] {
			entry := relayModel{Ladder: []stack.ModelHopRef{{Provider: sel.Provider, Model: sel.Model}}}
			return sel.Model, r.ladderFor(file, sel.Model, entry), nil
		}
	}
	names := make([]string, 0, len(file.Models))
	for name, entry := range file.Models {
		if r.granted(file, entry) {
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
	if hops, ok := r.ladders[name]; ok && file == r.reg {
		return hops
	}
	var hops []*modelHop
	for _, ref := range entry.Ladder {
		p := file.Providers[ref.Provider]
		if ref.Provider == "cliproxy" && p.KeyEnv == "" && r.auth != "" {
			p.KeyEnv = "OPENAI_API_KEY"
		}
		base := strings.TrimRight(p.URL, "/")
		if ref.Provider == "cliproxy" && base == "" {
			p.AllowPrivate = true
			base = "http://cliproxy:8317"
			if v := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_CLIPROXY")); v != "" {
				base = strings.TrimRight(v, "/")
			}
		}
		health := &modelHealth{}
		if file == r.reg {
			if shared := r.health[ref.Provider]; shared != nil {
				health = shared
			} else {
				r.health[ref.Provider] = health
			}
		}
		hop := &modelHop{modelHealth: health, name: ref.Provider, base: base, keyEnv: p.KeyEnv, model: ref.Model, models: ref.Models, reasoning: ref.Reasoning, owner: r.owner}
		// Declared limits split by measurability (SPEC-0092 R7.3): requests
		// and tokens meter locally; provider_quota needs the service's own
		// per-session telemetry — a cliproxy OAuth session readable through
		// the instance's management API. spend_usd has no price feed. A
		// service whose limits cannot be measured is disabled — drifting
		// unbounded is worse than parking it.
		for _, l := range ref.Limits {
			switch l.Of {
			case "requests", "tokens":
				hop.limits = append(hop.limits, l)
			case "provider_quota":
				if p.Via == "cliproxy" && p.UpstreamKind == "oauth-session" && strings.TrimSpace(os.Getenv("HUB_CLIPROXY_MGMT_KEY")) != "" {
					hop.quota = append(hop.quota, l)
				} else {
					hop.disabled = "limit_unmeasurable"
				}
			default: // spend_usd: no price feed exists
				hop.disabled = "limit_unmeasurable"
			}
		}
		if len(hop.limits) > 0 {
			hop.meter = r.usageMeter()
		}
		if grant := p.BrokerGrants[r.owner]; grant != "" {
			target := p.CredentialEnv
			actor := identity.Envelope{Schema: identity.Schema, PrincipalID: r.owner, ContextID: r.owner, RuntimeID: r.owner, ExternalIdentityID: r.owner, ConversationID: "model-relay", DeliveryTargetID: "model-relay", PolicyVersion: "model-relay"}
			binding := credentialbroker.ModelRelayBinding(r.owner, ref.Provider)
			hop.keyFor = func(ctx context.Context) (string, error) {
				// The dedicated grant pins binding and workload to the
				// relay's per-provider slot; both must be presented.
				return r.broker.ResolveEnv(ctx, actor, binding, binding, grant, target)
			}
		}
		hop.transport = netguard.Transport(p.AllowPrivate)
		if p.AllowPrivate {
			hop.transport = netguard.LocalTransport()
		}
		if transport, ok := hop.transport.(*http.Transport); ok {
			transport.Proxy = nil
		}
		hops = append(hops, hop)
	}
	if file == r.reg {
		r.ladders[name] = hops
	}
	return hops
}

// relaySelection is the owner's persisted choice: a logical model name, or a
// provider-side catalog model pinned to the granted account that serves it.
type relaySelection struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

func (r *relayRegistry) usageMeter() *relayMeter {
	if r.meter == nil {
		r.meter = &relayMeter{path: r.statePath + ".usage.json"}
	}
	return r.meter
}

func (r *relayRegistry) selection() relaySelection {
	b, err := os.ReadFile(r.statePath) // #nosec G304 -- state path is operator-mounted
	if err != nil {
		return relaySelection{}
	}
	var sel relaySelection
	if json.Unmarshal(b, &sel) != nil {
		return relaySelection{}
	}
	return sel
}

// serveSelect is the owner-facing model switch: admin-token authenticated,
// grant-checked and persisted into the mounted selection file. GET reports
// the effective selection so the owner can see what their traffic resolves to.
// grantsRight is the model-relay PEP (SPEC-0092 R2.2): when the space's
// ToolHub decide endpoint is wired, models.* rights are verified against
// the published ceiling and live ledger before the relay acts. An
// unconfigured decider keeps the legacy auth-only posture.
func (r *relayRegistry) grantsRight(ctx context.Context, operation, scope string) bool {
	if !r.decider.Enabled() {
		return true
	}
	allow, err := r.decider.GrantsRight(ctx, "models", operation, scope)
	if err != nil {
		log.Printf("model-relay rights decide owner=%s right=models.%s err=%v", r.owner, operation, err)
	}
	return allow
}

func (r *relayRegistry) serveSelect(w http.ResponseWriter, req *http.Request) {
	if r.admin == "" || req.Header.Get("Authorization") != "Bearer "+r.admin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if req.Method == http.MethodGet {
		sel := r.selection()
		if sel.Model == "" {
			sel.Model = "default"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sel)
		return
	}
	var sel relaySelection
	if json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&sel) != nil || sel.Model == "" {
		http.Error(w, "invalid model request", http.StatusBadRequest)
		return
	}
	if !r.grantsRight(req.Context(), "switch", sel.Model) {
		http.Error(w, "right denied", http.StatusForbidden)
		return
	}
	file, err := r.current()
	if err != nil {
		http.Error(w, "model registry unavailable", http.StatusBadGateway)
		return
	}
	level := r.switchLevel(file)
	if level == "none" {
		http.Error(w, "model switching not granted", http.StatusForbidden)
		return
	}
	if entry, ok := file.Models[sel.Model]; ok && r.granted(file, entry) {
		sel.Provider = ""
	} else if level == "catalog" {
		sel.Provider = r.catalogProvider(req.Context(), file, sel.Model)
		if sel.Provider == "" {
			http.Error(w, "model not granted", http.StatusForbidden)
			return
		}
		// A qualified "provider/model" argument pins the account but persists
		// the bare upstream id — it is the id the provider expects.
		if _, model, ok := strings.Cut(sel.Model, "/"); ok {
			sel.Model = model
		}
	} else {
		http.Error(w, "model not granted", http.StatusForbidden)
		return
	}
	body, _ := json.Marshal(sel)
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

// serveRegistry reports the owner's granted models with their ordered account
// ladders — the account view behind /model. Admin-bearer only: provider URLs,
// env key names and grants stay in the mounted file and never cross the wire.
func (r *relayRegistry) serveRegistry(w http.ResponseWriter, req *http.Request) {
	if r.admin == "" || req.Header.Get("Authorization") != "Bearer "+r.admin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	file, err := r.current()
	if err != nil {
		http.Error(w, "model registry unavailable", http.StatusBadGateway)
		return
	}
	type hop struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Down     bool   `json:"down,omitempty"`
	}
	type model struct {
		Name   string `json:"name"`
		Ladder []hop  `json:"ladder"`
	}
	models := make([]model, 0, len(file.Models))
	hopByProvider := map[string]*modelHop{}
	var providers []string
	for name, entry := range file.Models {
		if !r.granted(file, entry) {
			continue
		}
		ladder := make([]hop, 0, len(entry.Ladder))
		hops := r.ladderFor(file, name, entry)
		for i, ref := range entry.Ladder {
			h := hop{Provider: ref.Provider, Model: ref.Model}
			if health := r.health[ref.Provider]; health != nil && health.down(time.Now()) {
				h.Down = true
			}
			ladder = append(ladder, h)
			if _, seen := hopByProvider[ref.Provider]; !seen {
				providers = append(providers, ref.Provider)
				hopByProvider[ref.Provider] = nil
				if i < len(hops) && hops[i].name == ref.Provider {
					hopByProvider[ref.Provider] = hops[i]
				}
			}
		}
		models = append(models, model{Name: name, Ladder: ladder})
	}
	slices.SortFunc(models, func(a, b model) int { return strings.Compare(a.Name, b.Name) })
	type account struct {
		Provider string   `json:"provider"`
		Down     bool     `json:"down,omitempty"`
		Catalog  []string `json:"catalog,omitempty"`
	}
	accounts := make([]account, 0, len(providers))
	for _, name := range providers {
		a := account{Provider: name}
		if health := r.health[name]; health != nil && health.down(time.Now()) {
			a.Down = true
		}
		if upstream := hopByProvider[name]; upstream != nil {
			a.Catalog = r.providerCatalog(req.Context(), upstream)
		}
		accounts = append(accounts, a)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"models": models, "accounts": accounts, "switch": r.switchLevel(file)})
}

// providerCatalog lists what an upstream account can actually serve — the
// /v1/models of that provider, fetched with the hop's own credentials. The
// hop transport stays pinned and redirect-free like regular traffic; a
// failed or empty answer means no catalog rather than a relay error.
func (r *relayRegistry) providerCatalog(ctx context.Context, hop *modelHop) []string {
	target := hop.base + "/v1/models"
	if base, err := url.Parse(hop.base); err == nil && strings.Trim(base.Path, "/") != "" {
		target = hop.base + "/models"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil) // #nosec G704 -- configured upstream hop
	if err != nil {
		return nil
	}
	switch {
	case hop.keyFor != nil:
		key, err := hop.keyFor(ctx)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+key)
	case hop.keyEnv != "":
		if key := os.Getenv(hop.keyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	client := &http.Client{Transport: hop.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req) // #nosec G704 -- configured upstream hop
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc) != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out
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

// ModelRelayHandlerFromEnv picks the relay mode: a mounted models.yaml
// registry when HUB_MODEL_RELAY_REGISTRY is set, else the static env ladder.
func ModelRelayHandlerFromEnv() (http.Handler, error) {
	if _, _, err := modelRelayLimits(); err != nil {
		return nil, err
	}
	if registry := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_REGISTRY")); registry != "" {
		state := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_STATE"))
		if state == "" {
			state = filepath.Join(filepath.Dir(registry), "model-select.json")
		}
		admin := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN"))
		if file := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN_FILE")); admin == "" && file != "" {
			if b, err := os.ReadFile(file); err == nil { // #nosec G304 G703 -- token path is operator-mounted
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

func modelRelayLimits() (time.Duration, time.Duration, error) {
	// The hop bound covers the whole exchange incl. a streaming body — long
	// generations (a whole book rendered in one completion) legitimately run
	// minutes, so the default is the capped maximum rather than a tighter
	// ceiling; the total budget leaves room for one retried fast-failing hop.
	hop, total := 5*time.Minute, 6*time.Minute
	for _, item := range []struct {
		key     string
		value   *time.Duration
		maximum time.Duration
	}{
		{"HUB_MODEL_RELAY_HOP_TIMEOUT", &hop, 5 * time.Minute},
		{"HUB_MODEL_RELAY_TIMEOUT", &total, 10 * time.Minute},
	} {
		if raw := os.Getenv(item.key); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 || value > item.maximum {
				return 0, 0, fmt.Errorf("invalid %s bound", item.key)
			}
			*item.value = value
		}
	}
	if hop > total {
		return 0, 0, fmt.Errorf("model hop timeout exceeds total timeout")
	}
	return hop, total, nil
}

func runModelRelay(ctx context.Context) error {
	handler, err := ModelRelayHandlerFromEnv()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "0.0.0.0:8318", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
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

// requestNeedsReasoning reports whether the request carries reasoning
// parameters a service must declare support for.
func requestNeedsReasoning(body []byte) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return false
	}
	_, a := doc["reasoning_effort"]
	_, b := doc["reasoning"]
	_, c := doc["thinking"]
	return a || b || c
}
