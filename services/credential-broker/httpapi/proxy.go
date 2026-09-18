package httpapi

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"strings"

	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/safenet"
)

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, a identity.Actor, body []byte) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/runtime/proxy/"), "/", 3)
	if len(parts) < 2 {
		fail(w, broker.ErrInvalid)
		return
	}
	path := "/"
	if len(parts) == 3 {
		path += parts[2]
	}
	use, e := s.b.BeginUse(r.Context(), a, parts[0])
	if e != nil {
		fail(w, e)
		return
	}
	defer use.Finish()
	var route contract.Route
	found := false
	for _, candidate := range use.Contract.Routes {
		if candidate.ID == parts[1] {
			route = candidate
			found = true
			break
		}
	}
	if !found || !route.Allows(r.Method, path) {
		fail(w, broker.ErrDenied)
		return
	}
	values, e := s.b.Resolve(use.Context, use)
	if e != nil {
		fail(w, e)
		return
	}
	defer values.Wipe()
	target := strings.TrimSuffix(route.BaseURL, "/") + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, e := http.NewRequestWithContext(use.Context, r.Method, target, bytes.NewReader(body))
	if e != nil {
		fail(w, broker.ErrInvalid)
		return
	}
	// A positive header allowlist prevents cookies, caller auth, Host and proxy
	// headers from being forwarded. No arbitrary CONNECT or TLS interception exists.
	for _, h := range []string{"Accept", "Content-Type"} {
		if v := r.Header.Get(h); len(v) <= 1024 && !strings.ContainsAny(v, "\r\n\x00") {
			req.Header.Set(h, v)
		}
	}
	for h, v := range route.StaticHeaders {
		req.Header.Set(h, v)
	}
	secret := string(values[route.Field])
	if route.Auth != "mtls" && strings.ContainsAny(secret, "\r\n\x00") {
		fail(w, broker.ErrInvalid)
		return
	}
	netPolicy := s.cfg.NetworkPolicy
	switch route.Auth {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+secret)
	case "header":
		req.Header.Set(route.Header, secret)
	case "basic":
		password := string(values[route.PasswordField])
		if strings.ContainsAny(password, "\r\n\x00") {
			fail(w, broker.ErrInvalid)
			return
		}
		req.SetBasicAuth(secret, password)
	case "oauth2":
		secret, e = s.b.AccessToken(use.Context, use)
		if e != nil {
			fail(w, e)
			return
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	case "mtls":
		cert, e := tls.X509KeyPair(values[route.Field], values[route.PasswordField])
		if e != nil {
			fail(w, broker.ErrInvalid)
			return
		}
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if netPolicy.TLSConfig != nil {
			tc = netPolicy.TLSConfig.Clone()
		}
		tc.Certificates = []tls.Certificate{cert}
		netPolicy.TLSConfig = tc
	default:
		fail(w, broker.ErrInvalid)
		return
	}
	var client *http.Client
	cacheKey := use.Contract.Digest() + "/" + route.ID
	if cached, ok := s.clients.Load(cacheKey); ok && route.Auth != "mtls" {
		client = cached.(*http.Client)
	} else {
		client, e = safenet.New(route.BaseURL, netPolicy)
		if e != nil {
			fail(w, broker.ErrDenied)
			return
		}
		if route.Auth == "mtls" {
			defer client.CloseIdleConnections()
		} else {
			s.clients.Store(cacheKey, client)
		}
	}
	if !use.Valid() {
		fail(w, broker.ErrDenied)
		return
	}
	res, e := client.Do(req)
	if e != nil {
		fail(w, broker.ErrUnavailable)
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		fail(w, broker.ErrDenied)
		return
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		fail(w, broker.ErrUnavailable)
		return
	}
	defer clear(data)
	// Defense in depth for obvious echo endpoints, not a generic DLP claim.
	if len(secret) >= 8 && bytes.Contains(data, []byte(secret)) {
		fail(w, broker.ErrDenied)
		return
	}
	for _, v := range values {
		if len(v) >= 8 && bytes.Contains(data, v) {
			fail(w, broker.ErrDenied)
			return
		}
	}
	if !use.Valid() {
		fail(w, broker.ErrDenied)
		return
	}
	if ct := res.Header.Get("Content-Type"); len(ct) < 200 {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(data)
}
