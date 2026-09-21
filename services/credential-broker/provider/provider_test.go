package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/credential-broker/internal/safenet"
)

func local(t *testing.T) *Local {
	t.Helper()
	d := t.TempDir()
	_ = os.Chmod(d, 0700)
	p, e := NewLocal("local", d, bytes.Repeat([]byte{5}, 32))
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestLocalProvider(t *testing.T) {
	p := local(t)
	ctx := context.Background()
	secret := Bundle{"token": []byte("synthetic-canary-secret"), "binary": {0, 1, 2, 0xff}}
	ref, e := p.Write(ctx, secret)
	if e != nil {
		t.Fatal(e)
	}
	got, e := p.Read(ctx, ref)
	if e != nil || !bytes.Equal(got["binary"], secret["binary"]) {
		t.Fatal(e)
	}
	cp := got.Clone()
	cp["binary"][0] = 8
	if got["binary"][0] == 8 {
		t.Fatal("alias")
	}
	cp.Wipe()
	disk, _ := os.ReadFile(filepath.Join(p.dir, ref.Locator))
	if bytes.Contains(disk, secret["token"]) {
		t.Fatal("plaintext")
	}
	for _, r := range []Ref{{"other", ref.Locator, "1"}, {"local", "../key", "1"}, {"local", ref.Locator, "2"}, {"local", strings.Repeat("a", 43), "1"}} {
		if _, e = p.Read(ctx, r); e == nil {
			t.Fatal("bad ref")
		}
	}
	wrong := ref
	wrong.Locator = strings.Repeat("b", 43)
	_ = os.WriteFile(filepath.Join(p.dir, wrong.Locator), disk, 0600)
	if _, e = p.Read(ctx, wrong); e == nil {
		t.Fatal("AAD substitution")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = p.Read(canceled, ref); e == nil {
		t.Fatal("context")
	}
	if _, e = p.Write(canceled, secret); e == nil {
		t.Fatal("context")
	}
	if p.Delete(canceled, ref) == nil {
		t.Fatal("context")
	}
	if _, e = p.Write(ctx, Bundle{"big": make([]byte, 600<<10)}); e == nil {
		t.Fatal("size")
	}
	if e = p.Delete(ctx, Ref{Provider: "local", Locator: "../../", Version: "1"}); e == nil {
		t.Fatal("delete traversal")
	}
	if e = p.Delete(ctx, ref); e != nil {
		t.Fatal(e)
	}
	if e = p.Delete(ctx, ref); e != nil {
		t.Fatal("idempotent delete")
	}
	if _, e = p.Read(ctx, ref); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	r := Registry{"local": p}
	if _, e = r.Resolve(ctx, ref); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = r.Resolve(ctx, Ref{Provider: "missing"}); e == nil {
		t.Fatal("missing")
	}
	if _, e = r.Put(ctx, "missing", secret); e == nil {
		t.Fatal("missing")
	}
	if !p.Capabilities().Write {
		t.Fatal("capabilities")
	}
	if _, e := NewLocal("x", t.TempDir(), []byte("bad")); e == nil {
		t.Fatal("key")
	}
}
func tlsClient(t *testing.T, s *httptest.Server) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	c, e := safenet.New(s.URL, safenet.Policy{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLSConfig: &tls.Config{RootCAs: pool}})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestVaultKV2(t *testing.T) {
	mode := "normal"
	var stored map[string]any
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "fake-service-token" || r.Header.Get("X-Vault-Namespace") != "team" {
			t.Error("auth")
		}
		if mode == "404" {
			w.WriteHeader(404)
			return
		}
		if mode == "fail" {
			w.WriteHeader(500)
			return
		}
		if mode == "bad" {
			_, _ = w.Write([]byte(`{"data":`))
			return
		}
		switch r.Method {
		case "GET":
			if mode == "encoded" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": stored["data"]}})
			} else {
				_, _ = w.Write([]byte(`{"data":{"data":{"token":"synthetic-vault-token","config":{"project":"x"}},"metadata":{"version":7}}}`))
			}
		case "POST":
			_ = json.NewDecoder(r.Body).Decode(&stored)
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		case "DELETE":
			w.WriteHeader(204)
		}
	}))
	defer up.Close()
	p := &VaultKV2{ID: "vault", BaseURL: up.URL, Mount: "kv", Prefix: "broker", Namespace: "team", Client: tlsClient(t, up), TokenSource: func() (string, error) { return "fake-service-token", nil }, Writable: true}
	ctx := context.Background()
	ref := Ref{"vault", "existing", "7"}
	b, e := p.Read(ctx, ref)
	if e != nil || string(b["token"]) != "synthetic-vault-token" || string(b["config"]) != `{"project":"x"}` {
		t.Fatal(e, b)
	}
	newRef, e := p.Write(ctx, Bundle{"token": []byte("new-synthetic-token")})
	if e != nil || newRef.Version != "1" {
		t.Fatal(e)
	}
	options := stored["options"].(map[string]any)
	if options["cas"] != float64(0) {
		t.Fatal("CAS required")
	}
	mode = "encoded"
	b, e = p.Read(ctx, newRef)
	if e != nil || string(b["token"]) != "new-synthetic-token" {
		t.Fatal(e)
	}
	if p.Delete(ctx, ref) == nil {
		t.Fatal("external objects must not be destroyed")
	}
	if p.Delete(ctx, newRef) != nil {
		t.Fatal("delete managed")
	}
	for _, m := range []string{"404", "fail", "bad"} {
		mode = m
		if _, e = p.Read(ctx, ref); e == nil {
			t.Fatal(m)
		}
	}
	mode = "404"
	if p.Delete(ctx, newRef) != nil {
		t.Fatal("delete missing")
	}
	for _, bad := range []Ref{{"other", "x", "1"}, {"vault", "../../x", "1"}, {"vault", "x", "-1"}} {
		if _, e = p.Read(ctx, bad); e == nil {
			t.Fatal("bad locator")
		}
	}
	p.Writable = false
	if _, e = p.Write(ctx, Bundle{}); !errors.Is(e, ErrReadOnly) {
		t.Fatal(e)
	}
	if p.Delete(ctx, newRef) != ErrReadOnly {
		t.Fatal("read-only")
	}
	p.TokenSource = func() (string, error) { return "x\r\ny", nil }
	if _, e = p.Read(ctx, ref); e == nil {
		t.Fatal("header injection")
	}
}
func TestKubernetesReadOnly(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/team/secrets/google" || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Error("request")
		}
		_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"17"},"data":{"token":"ZmFrZS10b2tlbg=="}}`))
	}))
	defer up.Close()
	p := &Kubernetes{ID: "k8s", BaseURL: up.URL, Namespace: "team", Client: tlsClient(t, up), TokenSource: func() (string, error) { return "service-token", nil }}
	ctx := context.Background()
	ref := Ref{"k8s", "google", "17"}
	b, e := p.Read(ctx, ref)
	if e != nil || string(b["token"]) != "fake-token" {
		t.Fatal(e)
	}
	ref.Version = "18"
	if _, e = p.Read(ctx, ref); !errors.Is(e, ErrNotFound) {
		t.Fatal("version")
	}
	ref.Locator = "../other"
	if _, e = p.Read(ctx, ref); e == nil {
		t.Fatal("path")
	}
	if p.Capabilities().Write {
		t.Fatal("write")
	}
	if _, e = p.Write(ctx, Bundle{}); e != ErrReadOnly {
		t.Fatal(e)
	}
	if p.Delete(ctx, ref) != ErrReadOnly {
		t.Fatal("delete")
	}
	r := Registry{"k8s": p}
	if _, e = r.Put(ctx, "k8s", Bundle{}); e != ErrReadOnly {
		t.Fatal(e)
	}
}
