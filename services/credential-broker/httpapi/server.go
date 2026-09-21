// Package httpapi exposes separate authenticated control/runtime surfaces and
// an identity-bound browser form. No control response contains secret values.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/safenet"
	"github.com/letya999/credential-broker/internal/strictjson"
)

type Config struct {
	Broker   *broker.Broker
	Verifier identity.Verifier
	// DevHTTP disables Secure cookies ONLY on an explicit loopback HTTP origin.
	DevHTTP bool
	// APIHosts lists extra Host header values accepted on the authenticated /v1/
	// surface, e.g. an internal service name. Browser endpoints stay pinned to
	// the public origin host so DNS rebinding cannot reach the connect form.
	APIHosts      []string
	NetworkPolicy safenet.Policy
}
type Server struct {
	b        *broker.Broker
	cfg      Config
	host     string
	origin   string
	apiHosts []string
	slots    chan struct{}
	limits   *limiter
	clients  sync.Map
}

var apiHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// validAPIHost accepts a DNS name or IP literal with an optional numeric port.
func validAPIHost(v string) bool {
	host, port, e := net.SplitHostPort(v)
	if e != nil {
		host, port = v, ""
	}
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return apiHostPattern.MatchString(strings.ToLower(strings.TrimSuffix(host, "."))) || net.ParseIP(strings.Trim(host, "[]")) != nil
}

func New(cfg Config) (*Server, error) {
	if cfg.Broker == nil || len(cfg.Verifier.Keys) == 0 {
		return nil, broker.ErrInvalid
	}
	u, e := url.Parse(cfg.Broker.PublicOrigin())
	if e != nil {
		return nil, e
	}
	if cfg.DevHTTP {
		if u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
			return nil, broker.ErrInvalid
		}
	} else if u.Scheme != "https" {
		return nil, broker.ErrInvalid
	}
	if len(cfg.APIHosts) > 8 {
		return nil, broker.ErrInvalid
	}
	for _, h := range cfg.APIHosts {
		if !validAPIHost(h) || h == u.Host {
			return nil, broker.ErrInvalid
		}
	}
	return &Server{b: cfg.Broker, cfg: cfg, host: u.Host, origin: u.String(), apiHosts: cfg.APIHosts, slots: make(chan struct{}, 64), limits: newLimiter()}, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	if !s.cfg.DevHTTP {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	browserPath := strings.HasPrefix(r.URL.Path, "/connect/") || r.URL.Path == "/oauth/callback" || r.URL.Path == "/form.css"
	if r.Host != s.host && (browserPath || !slices.Contains(s.apiHosts, r.Host)) || !s.cfg.DevHTTP && r.TLS == nil {
		fail(w, broker.ErrDenied)
		return
	}
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "//") || strings.ContainsAny(r.URL.Path, "\\\x00") {
		fail(w, broker.ErrInvalid)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, broker.ErrUnavailable)
		return
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		if !s.b.Healthy() {
			fail(w, broker.ErrUnavailable)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/connect/") || r.URL.Path == "/oauth/callback" || r.URL.Path == "/form.css" {
		if !s.limits.allow(r.RemoteAddr) {
			writeJSON(w, 429, v1.Error{Code: "rate_limited"})
			return
		}
		s.browser(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	audience := "broker:control"
	if strings.HasPrefix(r.URL.Path, "/v1/runtime/") {
		audience = "broker:runtime"
	}
	if strings.HasPrefix(r.URL.Path, "/v1/requests/") && strings.HasSuffix(r.URL.Path, "/approve") {
		audience = "broker:approve"
	}
	limit := int64(64 << 10)
	if strings.HasPrefix(r.URL.Path, "/v1/runtime/proxy/") {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	data, e := io.ReadAll(r.Body)
	if e != nil {
		writeJSON(w, 413, v1.Error{Code: "body_too_large"})
		return
	}
	defer clear(data)
	h := r.Header.Values("Authorization")
	if len(h) != 1 || !strings.HasPrefix(h[0], "Bearer ") {
		fail(w, identity.ErrUnauthorized)
		return
	}
	a, e := s.cfg.Verifier.Verify(strings.TrimPrefix(h[0], "Bearer "), audience, r.Method, r.URL.RequestURI(), data)
	if e != nil {
		fail(w, e)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/runtime/proxy/") {
		s.proxy(w, r, a, data)
		return
	}
	if len(data) > 0 {
		mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || mt != "application/json" {
			fail(w, broker.ErrInvalid)
			return
		}
	}
	s.api(w, r, a, data)
}
func decode(data []byte, dst any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	if e := strictjson.Decode(data, dst); e != nil {
		return broker.ErrInvalid
	}
	return nil
}
func noBody(data []byte) bool {
	return len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("{}"))
}
func (s *Server) api(w http.ResponseWriter, r *http.Request, a identity.Actor, data []byte) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var out any
	var e error
	status := 200
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/contracts":
		out = s.b.Catalog()
	case r.Method == "GET" && r.URL.Path == "/v1/events":
		var after uint64
		after, e = strconv.ParseUint(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
		if e != nil {
			e = broker.ErrInvalid
		} else {
			out = s.b.Events(a, after, 100)
		}
	case r.Method == "POST" && r.URL.Path == "/v1/requests":
		var in v1.CreateRequest
		if e = decode(data, &in); e == nil {
			out, e = s.b.CreateRequest(r.Context(), a, in)
			status = 201
		}
	case len(parts) == 3 && parts[1] == "requests" && r.Method == "GET":
		out, e = s.b.GetRequest(a, parts[2])
	case len(parts) == 4 && parts[1] == "requests" && parts[3] == "approve" && r.Method == "POST":
		var in v1.Approve
		if e = decode(data, &in); e == nil {
			e = s.b.Approve(a, parts[2], in.Code)
		}
	case len(parts) == 4 && parts[1] == "requests" && parts[3] == "cancel" && r.Method == "POST":
		if !noBody(data) {
			e = broker.ErrInvalid
		} else {
			e = s.b.CancelRequest(a, parts[2])
		}
	case len(parts) == 3 && parts[1] == "credentials" && r.Method == "GET":
		out, e = s.b.GetCredential(a, parts[2])
	case len(parts) == 3 && parts[1] == "credentials" && r.Method == "DELETE":
		if !noBody(data) {
			e = broker.ErrInvalid
		} else {
			e = s.b.DeleteCredential(r.Context(), a, parts[2])
		}
	case len(parts) == 4 && parts[1] == "credentials" && parts[3] == "grants" && r.Method == "POST":
		var in v1.GrantRequest
		if e = decode(data, &in); e == nil {
			out, e = s.b.CreateGrant(a, parts[2], in)
			status = 201
		}
	case len(parts) == 4 && parts[1] == "credentials" && parts[3] == "revoke" && r.Method == "POST":
		if !noBody(data) {
			e = broker.ErrInvalid
		} else {
			e = s.b.RevokeCredential(a, parts[2])
		}
	case len(parts) == 4 && parts[1] == "grants" && parts[3] == "revoke" && r.Method == "POST":
		if !noBody(data) {
			e = broker.ErrInvalid
		} else {
			e = s.b.RevokeGrant(a, parts[2])
		}
	case r.Method == "POST" && r.URL.Path == "/v1/leases":
		var in v1.AcquireLease
		if e = decode(data, &in); e == nil {
			out, e = s.b.Acquire(a, in)
			status = 201
		}
	case len(parts) == 4 && parts[1] == "runtime" && parts[2] == "leases" && r.Method == "GET":
		out, e = s.b.InspectLease(a, parts[3])
	case len(parts) == 5 && parts[1] == "runtime" && parts[2] == "leases" && parts[4] == "materialize" && r.Method == "POST":
		if !noBody(data) {
			e = broker.ErrInvalid
		} else {
			out, e = s.b.Materialize(r.Context(), a, parts[3])
		}
	case len(parts) == 5 && parts[1] == "runtime" && parts[2] == "leases" && parts[4] == "release" && r.Method == "POST":
		var in v1.RuntimeRelease
		if e = decode(data, &in); e == nil {
			e = s.b.Release(r.Context(), a, parts[3], in)
		}
	case len(parts) == 5 && parts[1] == "runtime" && parts[2] == "leases" && parts[4] == "renew" && r.Method == "POST":
		var in struct {
			TTLSeconds int `json:"ttl_seconds"`
		}
		if e = decode(data, &in); e == nil {
			out, e = s.b.Renew(a, parts[3], in.TTLSeconds)
		}
	default:
		e = broker.ErrNotFound
	}
	if e != nil {
		fail(w, e)
		return
	}
	if out == nil {
		out = map[string]string{"status": "ok"}
	}
	writeJSON(w, status, out)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status, code := 503, "unavailable"
	switch {
	case errors.Is(e, identity.ErrUnauthorized):
		status, code = 401, "unauthorized"
	case errors.Is(e, broker.ErrDenied):
		status, code = 403, "denied"
	case errors.Is(e, broker.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(e, broker.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(e, broker.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(e, broker.ErrExpired):
		status, code = 410, "expired"
	case errors.Is(e, broker.ErrReauthorize):
		status, code = 409, "reauthorize_required"
	}
	writeJSON(w, status, v1.Error{Code: code})
}
func defaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

type bucket struct {
	tokens float64
	at     time.Time
}
type limiter struct {
	mu      sync.Mutex
	entries map[string]bucket
	now     func() time.Time
}

func newLimiter() *limiter { return &limiter{entries: map[string]bucket{}, now: time.Now} }
func (l *limiter) allow(remote string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ip := remote
	if i := strings.LastIndex(remote, ":"); i >= 0 {
		ip = remote[:i]
	}
	b, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= 1024 {
			for k, v := range l.entries {
				if now.Sub(v.at) > time.Minute {
					delete(l.entries, k)
				}
			}
		}
		if len(l.entries) >= 1024 {
			return false
		}
		b = bucket{tokens: 20, at: now}
	}
	b.tokens += now.Sub(b.at).Seconds() * 2
	if b.tokens > 20 {
		b.tokens = 20
	}
	b.at = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.entries[ip] = b
	return allowed
}
