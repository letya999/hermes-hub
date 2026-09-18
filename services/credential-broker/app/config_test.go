package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/provider"
)

func devConfig(t *testing.T) Config {
	t.Helper()
	root := filepath.Join(t.TempDir(), "installation")
	if e := Init(root); e != nil {
		t.Fatal(e)
	}
	c, e := ReadConfig(filepath.Join(root, "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestInitConfigAndRun(t *testing.T) {
	c := devConfig(t)
	root := filepath.Dir(c.MasterKeyFile)
	raw, e := os.ReadFile(c.MasterKeyFile)
	if e != nil || len(raw) != 32 {
		t.Fatal(e)
	}
	before := append([]byte{}, raw...)
	if e = Init(filepath.Dir(root)); e == nil {
		t.Fatal("keys overwritten")
	}
	raw, _ = os.ReadFile(c.MasterKeyFile)
	if !bytes.Equal(raw, before) {
		t.Fatal("init altered key")
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	c.Listen = ln.Addr().String()
	c.PublicOrigin = "http://" + c.Listen
	svc, e := Build(c)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx, ln) }()
	defer cancel()
	h := &http.Client{Timeout: 2 * time.Second}
	res, e := h.Get(c.PublicOrigin + "/healthz")
	if e != nil {
		t.Fatal(e)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	// Exercise the periodic expiry sweep as well as graceful shutdown.
	time.Sleep(1100 * time.Millisecond)
	cancel()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stalled")
	}
	if svc.Broker.Healthy() {
		t.Fatal("broker not closed")
	}
}
func TestConfigAndStartupRefusals(t *testing.T) {
	c := devConfig(t)
	if _, e := ReadConfig("relative.json"); e == nil {
		t.Fatal("relative")
	}
	if _, e := ReadConfig("/no-such-file"); e == nil {
		t.Fatal("missing")
	}
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	if e := WriteJSON(filepath.Join(root, "bad.json"), map[string]any{"unknown": true}); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadConfig(filepath.Join(root, "bad.json")); e == nil {
		t.Fatal("unknown config")
	}
	bad := c
	bad.Listen = "invalid"
	if e := WriteJSON(filepath.Join(root, "bad-listen.json"), bad); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadConfig(filepath.Join(root, "bad-listen.json")); e == nil {
		t.Fatal("listen")
	}
	bad = c
	bad.TrustedKeys = nil
	_ = WriteJSON(filepath.Join(root, "bad-keys.json"), bad)
	if _, e := ReadConfig(filepath.Join(root, "bad-keys.json")); e == nil {
		t.Fatal("keys")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Listen = "0.0.0.0:8787" }, func(c *Config) { c.MasterKeyFile = "relative" }, func(c *Config) { c.ContractsDir = "relative" }, func(c *Config) {
		c.Providers = []ProviderConfig{{ID: "provider", Type: "unknown", URL: "https://vault.example"}}
	}, func(c *Config) {
		c.TrustedKeys = []KeyConfig{{ID: "x", Issuer: "x", PublicKeyFile: "/missing", Audiences: []string{"broker:control"}}}
	}, func(c *Config) { c.TrustedKeys = append(c.TrustedKeys, c.TrustedKeys[0]) }, func(c *Config) {
		c.TrustedKeys = append([]KeyConfig{}, c.TrustedKeys...)
		c.TrustedKeys[0].Audiences = []string{"arbitrary"}
	}, func(c *Config) { c.ExternalAliases = []AliasConfig{{ID: "bad", PrincipalID: "../x"}} }, func(c *Config) { c.RuntimeDir = "relative" }, func(c *Config) { c.MaxLedgerBytes = 1 }, func(c *Config) { c.PublicOrigin = "http://evil.example" }, func(c *Config) { c.DevHTTP = false }} {
		bad = c
		change(&bad)
		if s, e := Build(bad); e == nil {
			_ = s.Broker.Close()
			t.Fatal("invalid config accepted")
		}
	}
	if e := Init("relative"); e == nil {
		t.Fatal("init relative")
	}
	if e := WriteJSON(filepath.Join(root, "invalid.json"), make(chan int)); e == nil {
		t.Fatal("marshal")
	}
	// Contract schema is checked before any listening socket opens.
	if e := securefs.Create(c.ContractsDir, "bad.json", []byte(`{"id":"bad"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Build(c); e == nil {
		t.Fatal("invalid contract accepted")
	}
}
func TestProviderCompositionAndProtectedFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	tokenFile := filepath.Join(root, "token")
	if e := os.WriteFile(tokenFile, []byte("service-token\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if s, e := secretFile(tokenFile); e != nil || s != "service-token" {
		t.Fatal(e)
	}
	if e := os.Chmod(tokenFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := secretFile(tokenFile); e == nil {
		t.Fatal("world readable")
	}
	_ = os.Chmod(tokenFile, 0600)
	for _, v := range []string{"", "a\nb"} {
		_ = os.WriteFile(tokenFile, []byte(v), 0600)
		if _, e := secretFile(tokenFile); e == nil {
			t.Fatal("bad token")
		}
	}
	_ = os.WriteFile(tokenFile, []byte("service-token"), 0600)
	if _, e := secretFile("/missing"); e == nil {
		t.Fatal("missing")
	}
	if _, e := readFile("relative", 10); e == nil {
		t.Fatal("relative")
	}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secrets/") {
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"1"},"data":{"token":"eA=="}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"data":{"token":"x"}}}`))
		}
	}))
	defer up.Close()
	caFile := filepath.Join(root, "ca.pem")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw}), 0600)
	configs := []ProviderConfig{{ID: "vault", Type: "vault_kv2", URL: up.URL, Mount: "kv", Prefix: "broker", Namespace: "team", TokenFile: tokenFile, CAFile: caFile, AllowedCIDRs: []string{"127.0.0.1/32"}}, {ID: "k8s", Type: "kubernetes", URL: up.URL, Namespace: "team", TokenFile: tokenFile, CAFile: caFile, AllowedCIDRs: []string{"127.0.0.1/32"}}}
	reg, e := assembleProviders(configs, make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"vault", "k8s"} {
		b, e := reg.Resolve(context.Background(), provider.Ref{Provider: id, Locator: "example"})
		if e != nil || string(b["token"]) != "x" {
			t.Fatal(id, e)
		}
	}
	for _, configs := range [][]ProviderConfig{{{ID: "../bad", Type: "local"}}, {{ID: "a", Type: "local", Directory: filepath.Join(root, "a")}, {ID: "a", Type: "local", Directory: filepath.Join(root, "b")}}, {{ID: "v", Type: "vault_kv2", URL: "http://vault.example"}}, {{ID: "v", Type: "vault_kv2", URL: "https://vault.example", AllowedCIDRs: []string{"0.0.0.0/0"}}}, {{ID: "v", Type: "vault_kv2", URL: "https://vault.example", CAFile: "/missing"}}, {{ID: "v", Type: "unknown", URL: "https://vault.example"}}} {
		if _, e := assembleProviders(configs, make([]byte, 32)); e == nil {
			t.Fatal("bad providers")
		}
	}
	badCA := filepath.Join(root, "bad-ca")
	_ = os.WriteFile(badCA, []byte("bad"), 0600)
	if _, e := netPolicy(badCA, nil); e == nil {
		t.Fatal("CA")
	}
}
func TestAliasesOAuthConfigurationAndSyntaxDefaults(t *testing.T) {
	c := devConfig(t)
	root := filepath.Dir(filepath.Dir(c.MasterKeyFile))
	c.MaxLedgerBytes = 0
	_ = WriteJSON(filepath.Join(root, "zero.json"), c)
	read, e := ReadConfig(filepath.Join(root, "zero.json"))
	if e != nil || read.MaxLedgerBytes != 64<<20 {
		t.Fatal(e)
	}
	c.MaxLedgerBytes = 64 << 20
	alias := AliasConfig{ID: "existing", Ref: provider.Ref{Provider: "local", Locator: strings.Repeat("a", 43), Version: "1"}, PrincipalID: "artem", ContextID: "artem", ContractID: "github-pat", ContractRevision: 1}
	c.ExternalAliases = []AliasConfig{alias}
	svc, e := Build(c)
	if e != nil {
		t.Fatal(e)
	}
	_ = svc.Broker.Close()
	c.ExternalAliases = append(c.ExternalAliases, alias)
	if _, e := Build(c); e == nil {
		t.Fatal("duplicate alias")
	}
	c.ExternalAliases = nil
	c.PublicOrigin = "https://credentials.example"
	c.OAuthProviders = []OAuthConfig{{ID: "google", AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", ClientID: "client", Scopes: []string{"read"}, AuthStyle: "none"}}
	// OAuth config resolves before HTTP-mode mismatch; this reaches its validation.
	if _, e := Build(c); e == nil {
		t.Fatal("HTTP mode mismatch")
	}
	c.OAuthProviders[0].AuthStyle = "post"
	if _, e := Build(c); e == nil {
		t.Fatal("missing OAuth client secret")
	}
	c.OAuthProviders[0].ClientSecret = &provider.Ref{Provider: "local", Locator: strings.Repeat("a", 43), Version: "1"}
	c.OAuthProviders[0].ClientSecretField = "secret"
	if _, e := Build(c); e == nil {
		t.Fatal("still HTTPS mismatch")
	}
	c.OAuthProviders = append(c.OAuthProviders, c.OAuthProviders[0])
	if _, e := Build(c); e == nil {
		t.Fatal("duplicate OAuth")
	}
}

func TestNativeTLSIdentityFiles(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	certFile := filepath.Join(root, "server.pem")
	keyFile := filepath.Join(root, "key.pem")
	cert := up.TLS.Certificates[0]
	raw, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600)
	cfg, e := loadTLS(certFile, keyFile)
	if e != nil || cfg.MinVersion < tls.VersionTLS12 || len(cfg.Certificates) != 1 {
		t.Fatal(e)
	}
	if _, e = loadTLS("/missing", keyFile); e == nil {
		t.Fatal("missing cert")
	}
	if _, e = loadTLS(certFile, "/missing"); e == nil {
		t.Fatal("missing key")
	}
	_ = os.Chmod(keyFile, 0644)
	if _, e = loadTLS(certFile, keyFile); e == nil {
		t.Fatal("key permissions")
	}
	_ = os.Chmod(keyFile, 0600)
	_ = os.WriteFile(keyFile, []byte("invalid"), 0600)
	if _, e = loadTLS(certFile, keyFile); e == nil {
		t.Fatal("key parsing")
	}
}
