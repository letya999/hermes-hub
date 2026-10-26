package hosting

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type responseAudit struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseAudit) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseAudit) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, e := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, e
}
func (w *responseAudit) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const cookieName = "__Host-hub-site"
const bootstrap = `<!doctype html><meta name="referrer" content="no-referrer"><title>Open shared site</title><body><script>
(async()=>{const token=location.hash.slice(1);history.replaceState(null,"",location.pathname+location.search);if(!token){document.body.textContent="Open the original share link.";return;}const r=await fetch("/_hosting/exchange",{method:"POST",headers:{"Content-Type":"text/plain"},body:token,credentials:"same-origin"});if(r.ok){location.reload()}else{document.body.textContent="Link unavailable."}})();
</script>`

func (s *Service) session(r record) string {
	expires := minTime(s.now().Add(time.Hour), minTime(r.ShareExpires, r.ExpiresAt)).Unix()
	payload := fmt.Sprintf("%s:%d:%d", r.Name, r.Revision, expires)
	mac := hmac.New(sha256.New, s.sessionKey[:])
	_, _ = mac.Write([]byte(payload))
	_, _ = mac.Write([]byte(r.TokenHash))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) sessionOK(r record, req *http.Request) bool {
	cookie, err := req.Cookie(cookieName)
	if err != nil || len(cookie.Value) > 300 || !s.now().Before(r.ShareExpires) {
		return false
	}
	text, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.sessionKey[:])
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte(r.TokenHash))
	if !hmac.Equal(mac.Sum(nil), got) {
		return false
	}
	parts := strings.Split(string(payload), ":")
	if len(parts) != 3 || parts[0] != r.Name || parts[1] != strconv.FormatUint(r.Revision, 10) {
		return false
	}
	seconds, err := strconv.ParseInt(parts[2], 10, 64)
	return err == nil && s.now().Before(time.Unix(seconds, 0))
}
func securityHeaders(w http.ResponseWriter, dynamic bool) {
	connect := "'none'"
	if dynamic {
		connect = "'self'"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src "+connect+"; object-src 'none'; base-uri 'none'; worker-src 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}
func (s *Service) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	tracked := &responseAudit{ResponseWriter: w}
	w = tracked
	securityHeaders(w, false)
	s.mu.Lock()
	if s.closed || s.storageFault || s.auditFault {
		s.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	name := ""
	for n, r := range s.state.Sites {
		if req.Host == strings.TrimPrefix(r.Origin, "https://") {
			name = n
			break
		}
	}
	r, ok := s.state.Sites[name]

	if !ok || r.State != "ready" || r.Access == "private" || !s.now().Before(r.ExpiresAt) {
		if !s.userRate.allow(s.now(), 100, 100) {
			s.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		err := s.audit("deny", "", 0, "not_found", 0)
		s.mu.Unlock()
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, req)
		return
	}

	now := s.now()
	bucket := s.siteRates[name]
	if bucket == nil {
		bucket = &rateBudget{}
		s.siteRates[name] = bucket
	}
	if !s.userRate.allow(now, 100, 100) || !bucket.allow(now, 20, 20) || len(s.inflight) >= 32 || s.activeSite(name) >= 8 {
		if s.audit("deny", name, r.Revision, "rate_limited", 0) != nil {
			s.mu.Unlock()
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", http.StatusTooManyRequests)
		return
	}
	auditDone, auditErr := s.beginAudit("serve", name, r.Revision)
	if auditErr != nil {
		s.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	req = req.WithContext(ctx)
	s.nextRequest++
	requestKey := s.nextRequest
	s.inflight[requestKey] = activeRequest{name, cancel}
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.inflight, requestKey)
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		_ = auditDone(strconv.Itoa(status), tracked.bytes)
		s.mu.Unlock()
	}()
	if req.URL.Path == "/_hosting/exchange" {
		if r.Access != "link" || req.Method != "POST" || req.Header.Get("Origin") != r.Origin || !s.now().Before(r.ShareExpires) {
			s.mu.Unlock()
			http.NotFound(w, req)
			return
		}
		s.mu.Unlock()
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 128))
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			http.Error(w, "invalid", http.StatusRequestEntityTooLarge)
			return
		}
		s.mu.Lock()
		current := s.state.Sites[name]
		valid := err == nil && current.Revision == r.Revision && current.State == "ready" && s.now().Before(current.ExpiresAt) && subtle.ConstantTimeCompare([]byte(digest(string(body))), []byte(r.TokenHash)) == 1
		if !valid {
			s.mu.Unlock()
			http.NotFound(w, req)
			return
		}
		value := s.session(r)
		expiry := minTime(s.now().Add(time.Hour), minTime(r.ShareExpires, r.ExpiresAt))
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expiry})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Access == "link" && !s.sessionOK(r, req) {
		s.mu.Unlock()
		if req.Method == "GET" && req.URL.Path == "/" {
			securityHeaders(w, true)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, bootstrap)
			return
		}
		http.NotFound(w, req)
		return
	}
	if r.Mode == "dynamic" {
		s.dynamicResponse(w, req, r)
		return
	}
	if req.Method != "GET" && req.Method != "HEAD" {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	clean := strings.TrimPrefix(req.URL.Path, "/")
	if clean == "" {
		clean = "index.html"
	}
	if !safePath(clean) || clean == "." {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	file, err := s.root.Open(r.Release + "/" + clean)
	if err != nil {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	s.mu.Unlock()
	defer file.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	typ := mime.TypeByExtension(path.Ext(clean))
	if typ == "" {
		typ = "application/octet-stream"
	}
	w.Header().Set("Content-Type", typ)
	if req.Method == "HEAD" {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		return
	}
	// Never hand a descriptor to a background writer: cancellation fences the next read.
	http.ServeContent(boundedResponse{w, ctx, s}, req.WithContext(ctx), clean, info.ModTime(), &cancelReader{ctx: ctx, ReadSeeker: file})
}

type cancelReader struct {
	ctx context.Context
	io.ReadSeeker
}

func (r *cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	return r.ReadSeeker.Read(p)
}

type activeRequest struct {
	name   string
	cancel context.CancelFunc
}

func (s *Service) activeSite(name string) int {
	n := 0
	for _, r := range s.inflight {
		if r.name == name {
			n++
		}
	}
	return n
}

func (s *Service) ControlHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(token) < 32 || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		decode := func(v any) bool {
			err := decoder.Decode(v)
			if err == nil {
				err = decoder.Decode(new(any))
				if err == io.EOF {
					return true
				}
			}
			status := http.StatusBadRequest
			var large *http.MaxBytesError
			if errors.As(err, &large) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "invalid", status)
			return false
		}
		var out any
		var err error
		switch r.URL.Path {
		case "/v1/publish":
			var in PublishRequest
			if !decode(&in) {
				return
			}
			out, err = s.Publish(r.Context(), s.Binding(), in)
		case "/v1/unpublish":
			var in UnpublishRequest
			if !decode(&in) {
				return
			}
			err = s.Unpublish(r.Context(), s.Binding(), in)
			out = map[string]bool{"removed": err == nil}
		case "/v1/list":
			var in struct{}
			if !decode(&in) {
				return
			}
			out = s.List()
		case "/v1/status":
			var in struct {
				Name string `json:"name"`
			}
			if !decode(&in) {
				return
			}
			out, err = s.Status(in.Name)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			status := 503
			switch err {
			case ErrDenied:
				status = 403
			case ErrInvalid:
				status = 400
			case ErrConflict:
				status = 409
			case ErrNotFound:
				status = 404
			case ErrQuota:
				status = 429
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// dynamicResponse keeps all application bytes behind the same admission gate.
func (s *Service) dynamicResponse(w http.ResponseWriter, req *http.Request, r record) {
	if req.Method == "CONNECT" || req.Header.Get("Upgrade") != "" {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	if req.Method != "GET" && req.Method != "HEAD" && (r.Access == "link" || req.Header.Get("Cookie") != "" || req.Header.Get("Origin") != "") && req.Header.Get("Origin") != r.Origin {
		s.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	if s.executor == nil {
		s.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx := req.Context()
	executor := s.executor
	workload := s.workload(r)
	s.mu.Unlock()
	select {
	case s.relaySlots <- struct{}{}:
		defer func() { <-s.relaySlots }()
	case <-ctx.Done():
		http.Error(w, "application unavailable", http.StatusGatewayTimeout)
		return
	}
	req = req.Clone(ctx)
	req.Body = http.MaxBytesReader(w, req.Body, 1<<20)
	response, err := executor.RoundTrip(ctx, workload, req)
	if err != nil {
		status := 503
		if errors.Is(err, ErrQuota) {
			status = 413
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = 504
		}
		http.Error(w, "application unavailable", status)
		return
	}
	defer response.Body.Close()
	securityHeaders(w, true)
	for _, key := range []string{"Content-Type", "ETag", "Last-Modified"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	if location := response.Header.Get("Location"); location != "" {
		parsed, err := url.Parse(location)
		if err != nil || parsed.IsAbs() && parsed.Scheme+"://"+parsed.Host != r.Origin || parsed.Host != "" && !parsed.IsAbs() {
			http.Error(w, "invalid application redirect", http.StatusBadGateway)
			return
		}
		w.Header().Set("Location", location)
	}
	for _, cookie := range response.Cookies() {
		if cookie := appResponseCookie(workload, cookie); cookie != nil {
			http.SetCookie(w, cookie)
		}
	}
	w.WriteHeader(response.StatusCode)
	if req.Method != "HEAD" {
		_, _ = io.Copy(&bandwidthWriter{ctx: ctx, service: s, out: w}, io.LimitReader(response.Body, 8<<20))
	}
}

type bandwidthWriter struct {
	ctx     context.Context
	service *Service
	out     io.Writer
}

func (w *bandwidthWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), 64<<10)
		w.service.bandwidthMu.Lock()
		now := time.Now()
		at := w.service.bandwidthNext
		if at.Before(now) {
			at = now
		}
		w.service.bandwidthNext = at.Add(time.Duration(n) * time.Second / (10 << 20))
		w.service.bandwidthMu.Unlock()
		if delay := time.Until(at); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-w.ctx.Done():
				timer.Stop()
				return total, w.ctx.Err()
			case <-timer.C:
			}
		}
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		written, err := w.out.Write(p[:n])
		total += written
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		p = p[n:]
	}
	return total, nil
}

type boundedResponse struct {
	http.ResponseWriter
	ctx     context.Context
	service *Service
}

func (w boundedResponse) Write(p []byte) (int, error) {
	return (&bandwidthWriter{ctx: w.ctx, service: w.service, out: w.ResponseWriter}).Write(p)
}
func (w boundedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
