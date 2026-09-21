// Package client is the ToolHub/Communication Hub/runtime integration client.
// It signs each exact method, URI and body using the existing canonical identity.
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
)

type Signer struct {
	KeyID, Issuer, Audience string
	PrivateKey              ed25519.PrivateKey
}
type Client struct {
	base   string
	signer Signer
	actor  identity.Actor
	http   *http.Client
	now    func() time.Time
}
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	return "credential broker: " + e.Code + " (" + strconv.Itoa(e.Status) + ")"
}
func New(base string, signer Signer, actor identity.Actor, h *http.Client) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || len(signer.PrivateKey) != ed25519.PrivateKeySize || !actor.Valid() {
		return nil, errors.New("invalid client configuration")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, errors.New("HTTPS required")
	}
	if signer.Audience != "broker:control" && signer.Audience != "broker:approve" && signer.Audience != "broker:runtime" {
		return nil, errors.New("invalid audience")
	}
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second}
	}
	copy := *h
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout == 0 {
		copy.Timeout = 20 * time.Second
	}
	return &Client{base: base, signer: signer, actor: actor, http: &copy, now: time.Now}, nil
}
func (c *Client) request(ctx context.Context, method, path string, raw []byte, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/v1/") || strings.Contains(path, "\\") || strings.ContainsAny(path, "\r\n\x00") || strings.Contains(path, "#") {
		return nil, errors.New("invalid API path")
	}
	r, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	now := c.now()
	token, e := identity.Sign(c.signer.PrivateKey, identity.Claims{KeyID: c.signer.KeyID, Issuer: c.signer.Issuer, Audience: c.signer.Audience, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Method: method, RequestURI: r.URL.RequestURI(), BodySHA256: identity.Hash(raw), Actor: c.actor})
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(r)
}

// Do is for JSON contracts. It returns bounded, sanitized API errors, not bodies.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var raw []byte
	var e error
	if in != nil {
		raw, e = json.Marshal(in)
		if e != nil {
			return e
		}
	}
	defer clear(raw)
	res, e := c.request(ctx, method, path, raw, "application/json")
	if e != nil {
		return errors.New("credential broker transport failed")
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 {
		return errors.New("invalid broker response")
	}
	defer clear(data)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var er v1.Error
		_ = json.Unmarshal(data, &er)
		allowed := map[string]bool{"unauthorized": true, "denied": true, "not_found": true, "invalid_request": true, "conflict": true, "expired": true, "reauthorize_required": true, "unavailable": true, "rate_limited": true, "body_too_large": true}
		if !allowed[er.Code] {
			er.Code = "request_failed"
		}
		return &APIError{res.StatusCode, er.Code}
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return errors.New("invalid broker response")
	}
	return nil
}
func (c *Client) CreateRequest(ctx context.Context, in v1.CreateRequest) (out v1.Request, e error) {
	e = c.Do(ctx, "POST", "/v1/requests", in, &out)
	return
}
func (c *Client) Request(ctx context.Context, id string) (out v1.Request, e error) {
	e = c.Do(ctx, "GET", "/v1/requests/"+id, nil, &out)
	return
}
func (c *Client) Approve(ctx context.Context, id, code string) error {
	return c.Do(ctx, "POST", "/v1/requests/"+id+"/approve", v1.Approve{Code: code}, nil)
}
func (c *Client) Grant(ctx context.Context, id string, in v1.GrantRequest) (out v1.Grant, e error) {
	e = c.Do(ctx, "POST", "/v1/credentials/"+id+"/grants", in, &out)
	return
}
func (c *Client) Acquire(ctx context.Context, in v1.AcquireLease) (out v1.Lease, e error) {
	e = c.Do(ctx, "POST", "/v1/leases", in, &out)
	return
}
func (c *Client) Events(ctx context.Context, after uint64) (out v1.Events, e error) {
	e = c.Do(ctx, "GET", "/v1/events?after="+strconv.FormatUint(after, 10), nil, &out)
	return
}
func (c *Client) Contracts(ctx context.Context) (out []contract.Contract, e error) {
	e = c.Do(ctx, "GET", "/v1/contracts", nil, &out)
	return
}
func (c *Client) Revoke(ctx context.Context, id string) error {
	return c.Do(ctx, "POST", "/v1/credentials/"+id+"/revoke", nil, nil)
}

// Materialize is runtime-only. Never pass its result to an LLM or job ledger.
func (c *Client) Materialize(ctx context.Context, id string) (out v1.Materialized, e error) {
	e = c.Do(ctx, "POST", "/v1/runtime/leases/"+id+"/materialize", nil, &out)
	return
}
func (c *Client) Release(ctx context.Context, id string, in v1.RuntimeRelease) error {
	return c.Do(ctx, "POST", "/v1/runtime/leases/"+id+"/release", in, nil)
}
func (c *Client) Renew(ctx context.Context, id string, seconds int) (out v1.Lease, e error) {
	e = c.Do(ctx, "POST", "/v1/runtime/leases/"+id+"/renew", map[string]int{"ttl_seconds": seconds}, &out)
	return
}
