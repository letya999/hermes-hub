// Package app wires adapters. Domain code never imports this composition root.
package app

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/httpapi"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/safenet"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
	"github.com/letya999/credential-broker/materialize"
	"github.com/letya999/credential-broker/provider"
)

type KeyConfig struct {
	ID            string   `json:"id"`
	Issuer        string   `json:"issuer"`
	PublicKeyFile string   `json:"public_key_file"`
	Audiences     []string `json:"audiences"`
}
type ProviderConfig struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Directory    string   `json:"directory,omitempty"`
	URL          string   `json:"url,omitempty"`
	Mount        string   `json:"mount,omitempty"`
	Prefix       string   `json:"prefix,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	TokenFile    string   `json:"token_file,omitempty"`
	CAFile       string   `json:"ca_file,omitempty"`
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	Writable     bool     `json:"writable,omitempty"`
}
type AliasConfig struct {
	ID               string       `json:"id"`
	Ref              provider.Ref `json:"ref"`
	PrincipalID      string       `json:"principal_id"`
	ContextID        string       `json:"context_id"`
	ContractID       string       `json:"contract_id"`
	ContractRevision int          `json:"contract_revision"`
}
type OAuthConfig struct {
	ID                  string            `json:"id"`
	AuthorizeURL        string            `json:"authorize_url"`
	TokenURL            string            `json:"token_url"`
	ClientID            string            `json:"client_id"`
	Scopes              []string          `json:"scopes"`
	AuthStyle           string            `json:"auth_style"`
	ClientSecret        *provider.Ref     `json:"client_secret,omitempty"`
	ClientSecretField   string            `json:"client_secret_field,omitempty"`
	AuthorizeParameters map[string]string `json:"authorize_parameters,omitempty"`
}
type Config struct {
	Listen          string           `json:"listen"`
	PublicOrigin    string           `json:"public_origin"`
	DataDir         string           `json:"data_dir"`
	RuntimeDir      string           `json:"runtime_dir"`
	MasterKeyFile   string           `json:"master_key_file"`
	ContractsDir    string           `json:"contracts_dir"`
	TLSCertFile     string           `json:"tls_cert_file,omitempty"`
	TLSKeyFile      string           `json:"tls_key_file,omitempty"`
	DevHTTP         bool             `json:"dev_http"`
	MaxLedgerBytes  int64            `json:"max_ledger_bytes"`
	TrustedKeys     []KeyConfig      `json:"trusted_keys"`
	Providers       []ProviderConfig `json:"providers"`
	ExternalAliases []AliasConfig    `json:"external_aliases,omitempty"`
	OAuthProviders  []OAuthConfig    `json:"oauth_providers,omitempty"`
}

func ReadConfig(path string) (Config, error) {
	var c Config
	if !filepath.IsAbs(path) {
		return c, errors.New("configuration path must be absolute")
	}
	data, e := securefs.Read(filepath.Dir(path), filepath.Base(path), 1<<20)
	if e != nil {
		return c, errors.New("cannot read configuration")
	}
	if strictjson.Decode(data, &c) != nil {
		return c, errors.New("invalid configuration JSON")
	}
	if c.MaxLedgerBytes == 0 {
		c.MaxLedgerBytes = 64 << 20
	}
	if _, _, e := net.SplitHostPort(c.Listen); e != nil {
		return c, errors.New("invalid listen address")
	}
	if len(c.TrustedKeys) == 0 || len(c.TrustedKeys) > 64 || len(c.Providers) == 0 || len(c.Providers) > 32 {
		return c, errors.New("invalid adapter configuration")
	}
	return c, nil
}
func readFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, securefs.ErrUnsafe
	}
	return securefs.Read(filepath.Dir(path), filepath.Base(path), limit)
}
func secretFile(path string) (string, error) {
	fi, e := os.Lstat(path)
	if e != nil || fi.Mode().Perm()&0077 != 0 {
		return "", provider.ErrUnavailable
	}
	raw, e := readFile(path, 64<<10)
	if e != nil {
		return "", provider.ErrUnavailable
	}
	defer clear(raw)
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", provider.ErrUnavailable
	}
	return value, nil
}
func netPolicy(ca string, cidrs []string) (safenet.Policy, error) {
	p := safenet.Policy{}
	var e error
	p.AllowedCIDRs, e = safenet.ParseExceptions(cidrs)
	if e != nil {
		return p, e
	}
	if ca != "" {
		raw, e := readFile(ca, 1<<20)
		if e != nil {
			return p, e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return p, errors.New("invalid CA bundle")
		}
		p.TLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return p, nil
}
func assembleProviders(config []ProviderConfig, master []byte) (provider.Registry, error) {
	out := provider.Registry{}
	for _, p := range config {
		if !identity.ValidID(p.ID) || out[p.ID] != nil {
			return nil, errors.New("duplicate or invalid provider ID")
		}
		if p.Type == "local" {
			l, e := provider.NewLocal(p.ID, p.Directory, seal.Derive(master, "provider/"+p.ID))
			if e != nil {
				return nil, e
			}
			out[p.ID] = l
			continue
		}
		np, e := netPolicy(p.CAFile, p.AllowedCIDRs)
		if e != nil {
			return nil, e
		}
		client, e := safenet.New(p.URL, np)
		if e != nil {
			return nil, e
		}
		tokenPath := p.TokenFile
		tokenSource := func() (string, error) { return secretFile(tokenPath) }
		switch p.Type {
		case "vault_kv2":
			out[p.ID] = &provider.VaultKV2{ID: p.ID, BaseURL: p.URL, Mount: p.Mount, Prefix: p.Prefix, Namespace: p.Namespace, Client: client, TokenSource: tokenSource, Writable: p.Writable}
		case "kubernetes":
			out[p.ID] = &provider.Kubernetes{ID: p.ID, BaseURL: p.URL, Namespace: p.Namespace, Client: client, TokenSource: tokenSource}
		default:
			return nil, errors.New("unknown provider type")
		}
	}
	return out, nil
}

type Service struct {
	Broker  *broker.Broker
	Handler *httpapi.Server
	TLS     *tls.Config
	Config  Config
}

func Build(c Config) (*Service, error) {
	if c.DevHTTP {
		h, _, e := net.SplitHostPort(c.Listen)
		if e != nil || h != "127.0.0.1" && h != "::1" {
			return nil, errors.New("development must bind loopback")
		}
	} else {
		if os.Geteuid() == 0 {
			return nil, errors.New("production must run as non-root")
		}
		if c.TLSCertFile == "" || c.TLSKeyFile == "" {
			return nil, errors.New("production requires native TLS")
		}
	}
	master, e := securefs.ReadKey(c.MasterKeyFile)
	if e != nil {
		return nil, e
	}
	defer clear(master)
	providers, e := assembleProviders(c.Providers, master)
	if e != nil {
		return nil, e
	}
	if e := securefs.Dir(c.ContractsDir); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(c.ContractsDir)
	if e != nil {
		return nil, e
	}
	var contracts []contract.Contract
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, e := securefs.Read(c.ContractsDir, entry.Name(), 128<<10)
		if e != nil {
			return nil, e
		}
		var cc contract.Contract
		if strictjson.Decode(raw, &cc) != nil || cc.Validate() != nil {
			return nil, errors.New("invalid reviewed contract")
		}
		contracts = append(contracts, cc)
	}
	verifier := identity.Verifier{Keys: map[string]identity.TrustedKey{}}
	for _, kc := range c.TrustedKeys {
		raw, e := readFile(kc.PublicKeyFile, 33)
		if e != nil || len(raw) != ed25519.PublicKeySize || kc.ID == "" || kc.Issuer == "" || len(kc.Audiences) == 0 {
			return nil, errors.New("invalid trusted signing key")
		}
		if _, exists := verifier.Keys[kc.ID]; exists {
			return nil, errors.New("duplicate signing key")
		}
		for _, aud := range kc.Audiences {
			if aud != "broker:control" && aud != "broker:approve" && aud != "broker:runtime" {
				return nil, errors.New("unknown assertion audience")
			}
		}
		verifier.Keys[kc.ID] = identity.TrustedKey{PublicKey: raw, Issuer: kc.Issuer, Audiences: kc.Audiences}
	}
	aliases := map[string]broker.ExternalAlias{}
	for _, a := range c.ExternalAliases {
		if !identity.ValidID(a.ID) || !identity.ValidID(a.PrincipalID) || !identity.ValidID(a.ContextID) || providers[a.Ref.Provider] == nil {
			return nil, errors.New("invalid external alias")
		}
		if _, ok := aliases[a.ID]; ok {
			return nil, errors.New("duplicate external alias")
		}
		aliases[a.ID] = broker.ExternalAlias{Ref: a.Ref, PrincipalID: a.PrincipalID, ContextID: a.ContextID, ContractKey: fmt.Sprintf("%s@%d", a.ContractID, a.ContractRevision)}
	}
	oauth := map[string]*broker.OAuthProvider{}
	for _, oc := range c.OAuthProviders {
		if !identity.ValidID(oc.ID) || oauth[oc.ID] != nil {
			return nil, errors.New("invalid OAuth provider ID")
		}
		client, e := safenet.New(oc.TokenURL, safenet.Policy{})
		if e != nil {
			return nil, e
		}
		p := &broker.OAuthProvider{ID: oc.ID, AuthorizeURL: oc.AuthorizeURL, TokenURL: oc.TokenURL, ClientID: oc.ClientID, RedirectURL: c.PublicOrigin + "/oauth/callback", Scopes: oc.Scopes, AuthStyle: oc.AuthStyle, Client: client, AuthorizeParameters: oc.AuthorizeParameters}
		if oc.ClientSecret != nil {
			ref := *oc.ClientSecret
			field := oc.ClientSecretField
			p.SecretSource = func(ctx context.Context) (string, error) {
				values, e := providers.Resolve(ctx, ref)
				if e != nil {
					return "", e
				}
				defer values.Wipe()
				if len(values[field]) == 0 {
					return "", provider.ErrNotFound
				}
				return string(values[field]), nil
			}
		}
		if p.Validate() != nil {
			return nil, errors.New("invalid OAuth configuration")
		}
		oauth[oc.ID] = p
	}
	var tlsConfig *tls.Config
	if !c.DevHTTP {
		tlsConfig, e = loadTLS(c.TLSCertFile, c.TLSKeyFile)
		if e != nil {
			return nil, e
		}
	}
	ledger, e := journal.Open(c.DataDir, seal.Derive(master, "ledger"), c.MaxLedgerBytes)
	if e != nil {
		return nil, e
	}
	m, e := materialize.New(c.RuntimeDir, !c.DevHTTP)
	if e != nil {
		_ = ledger.Close()
		return nil, e
	}
	b, e := broker.New(broker.Config{Contracts: contracts, Providers: providers, ExternalAliases: aliases, Journal: ledger, Materializer: m, PublicOrigin: c.PublicOrigin, SessionKey: seal.Derive(master, "browser"), OAuth: oauth})
	if e != nil {
		_ = m.Close()
		_ = ledger.Close()
		return nil, e
	}
	handler, e := httpapi.New(httpapi.Config{Broker: b, Verifier: verifier, DevHTTP: c.DevHTTP})
	if e != nil {
		_ = b.Close()
		return nil, e
	}
	return &Service{Broker: b, Handler: handler, TLS: tlsConfig, Config: c}, nil
}
func (s *Service) Run(ctx context.Context, listener net.Listener) error {
	server := &http.Server{Handler: s.Handler, TLSConfig: s.TLS, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() {
		var e error
		if s.Config.DevHTTP {
			e = server.Serve(listener)
		} else {
			e = server.ServeTLS(listener, "", "")
		}
		result <- e
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var err error
run:
	for {
		select {
		case <-ctx.Done():
			break run
		case err = <-result:
			break run
		case <-ticker.C:
			s.Broker.Sweep()
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := server.Shutdown(shutdown); e != nil {
		_ = server.Close()
		if err == nil {
			err = e
		}
	}
	if e := s.Broker.Close(); e != nil && err == nil {
		err = e
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func WriteJSON(path string, v any) error {
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	return securefs.Create(filepath.Dir(path), filepath.Base(path), raw, 0600)
}

func loadTLS(certFile, keyFile string) (*tls.Config, error) {
	certBytes, e := readFile(certFile, 256<<10)
	if e != nil {
		return nil, e
	}
	fi, e := os.Lstat(keyFile)
	if e != nil || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("TLS key permissions must be 0600")
	}
	keyBytes, e := readFile(keyFile, 64<<10)
	if e != nil {
		return nil, e
	}
	defer clear(keyBytes)
	cert, e := tls.X509KeyPair(certBytes, keyBytes)
	if e != nil {
		return nil, errors.New("invalid TLS identity")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
