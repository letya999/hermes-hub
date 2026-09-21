package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/strictjson"
)

var remotePart = regexp.MustCompile(`^[a-zA-Z0-9_-][a-zA-Z0-9_.-]{0,127}$`)

func safePath(p string) bool {
	if p == "" || len(p) > 400 {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if !remotePart.MatchString(s) || s == "." || s == ".." {
			return false
		}
	}
	return true
}
func response(client *http.Client, req *http.Request, dst any) error {
	res, e := client.Do(req)
	if e != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 512<<10+1))
	if e != nil || len(b) > 512<<10 {
		return ErrUnavailable
	}
	defer clear(b)
	if len(b) > 0 && dst != nil {
		if !strictjson.Valid(b) || json.Unmarshal(b, dst) != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// VaultKV2 reads existing KV-v2 objects and optionally writes new immutable paths.
// TokenSource must read a protected file or agent; it is never an API parameter.
type VaultKV2 struct {
	ID, BaseURL, Mount, Prefix, Namespace string
	Client                                *http.Client
	TokenSource                           func() (string, error)
	Writable                              bool
}

func (v *VaultKV2) Capabilities() Capabilities {
	return Capabilities{v.Writable, v.Writable, true, true}
}
func (v *VaultKV2) request(ctx context.Context, method, kind, loc, version string, body []byte) (*http.Request, error) {
	if !remotePart.MatchString(v.Mount) || !safePath(v.Prefix) || !safePath(loc) || v.Client == nil || v.TokenSource == nil {
		return nil, ErrUnavailable
	}
	if strings.ContainsAny(v.Namespace, "\r\n\x00") {
		return nil, ErrUnavailable
	}
	u := strings.TrimSuffix(v.BaseURL, "/") + "/v1/" + v.Mount + "/" + kind + "/" + v.Prefix + "/" + loc
	if version != "" {
		if _, e := strconv.ParseUint(version, 10, 32); e != nil {
			return nil, ErrNotFound
		}
		u += "?version=" + version
	}
	r, e := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if e != nil {
		return nil, ErrUnavailable
	}
	token, e := v.TokenSource()
	if e != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrUnavailable
	}
	r.Header.Set("X-Vault-Token", token)
	if v.Namespace != "" {
		r.Header.Set("X-Vault-Namespace", v.Namespace)
	}
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}
func (v *VaultKV2) Read(ctx context.Context, ref Ref) (Bundle, error) {
	if ref.Provider != v.ID {
		return nil, ErrNotFound
	}
	r, e := v.request(ctx, "GET", "data", ref.Locator, ref.Version, nil)
	if e != nil {
		return nil, e
	}
	var doc struct {
		Data struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if e = response(v.Client, r, &doc); e != nil {
		return nil, e
	}
	if marker, ok := doc.Data.Data["_broker_encoding"]; ok && string(marker) == `"base64-v1"` {
		var b Bundle
		if json.Unmarshal(doc.Data.Data["values"], &b) != nil || b == nil {
			return nil, ErrUnavailable
		}
		return b, nil
	}
	b := Bundle{}
	for k, raw := range doc.Data.Data {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			b[k] = []byte(s)
		} else {
			b[k] = append([]byte(nil), raw...)
		}
	}
	return b, nil
}
func (v *VaultKV2) Write(ctx context.Context, b Bundle) (Ref, error) {
	if !v.Writable {
		return Ref{}, ErrReadOnly
	}
	id := seal.Random()
	raw, e := json.Marshal(map[string]any{"options": map[string]any{"cas": 0}, "data": map[string]any{"_broker_encoding": "base64-v1", "values": b}})
	if e != nil {
		return Ref{}, ErrUnavailable
	}
	defer clear(raw)
	r, e := v.request(ctx, "POST", "data", id, "", raw)
	if e != nil {
		return Ref{}, e
	}
	var doc struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	if e = response(v.Client, r, &doc); e != nil {
		return Ref{}, e
	}
	if doc.Data.Version < 1 {
		return Ref{}, ErrUnavailable
	}
	return Ref{v.ID, id, strconv.Itoa(doc.Data.Version)}, nil
}
func (v *VaultKV2) Delete(ctx context.Context, ref Ref) error {
	if !v.Writable {
		return ErrReadOnly
	}
	if ref.Provider != v.ID || !locatorRE.MatchString(ref.Locator) {
		return ErrNotFound
	}
	// Only broker-generated paths are deletable. External imports are never deleted.
	r, e := v.request(ctx, "DELETE", "metadata", ref.Locator, "", nil)
	if e != nil {
		return e
	}
	e = response(v.Client, r, nil)
	if e == ErrNotFound {
		return nil
	}
	return e
}

// Kubernetes is read-only, fixed-namespace and Get-only: no list/watch or RBAC writes.
type Kubernetes struct {
	ID, BaseURL, Namespace string
	Client                 *http.Client
	TokenSource            func() (string, error)
}

func (*Kubernetes) Capabilities() Capabilities                 { return Capabilities{false, false, true, true} }
func (*Kubernetes) Write(context.Context, Bundle) (Ref, error) { return Ref{}, ErrReadOnly }
func (*Kubernetes) Delete(context.Context, Ref) error          { return ErrReadOnly }
func (k *Kubernetes) Read(ctx context.Context, ref Ref) (Bundle, error) {
	if ref.Provider != k.ID || !remotePart.MatchString(k.Namespace) || !remotePart.MatchString(ref.Locator) || k.Client == nil || k.TokenSource == nil {
		return nil, ErrNotFound
	}
	u := strings.TrimSuffix(k.BaseURL, "/") + "/api/v1/namespaces/" + url.PathEscape(k.Namespace) + "/secrets/" + url.PathEscape(ref.Locator)
	r, e := http.NewRequestWithContext(ctx, "GET", u, nil)
	if e != nil {
		return nil, ErrUnavailable
	}
	token, e := k.TokenSource()
	if e != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrUnavailable
	}
	r.Header.Set("Authorization", "Bearer "+token)
	var doc struct {
		Data     Bundle `json:"data"`
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if e = response(k.Client, r, &doc); e != nil {
		return nil, e
	}
	if doc.Data == nil || ref.Version != "" && ref.Version != doc.Metadata.ResourceVersion {
		return nil, ErrNotFound
	}
	return doc.Data, nil
}
