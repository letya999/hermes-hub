package dns

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// gcloudClient speaks Cloud DNS v1 against a managed zone, authenticating
// with a service-account JWT bearer exchange per call.
type gcloudClient struct {
	cfg  *Config
	sa   gcloudSA
	http *http.Client
	base string
}

type gcloudSA struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func newGcloud(cfg *Config, creds map[string]string, opts Options) (*gcloudClient, error) {
	var sa gcloudSA
	if err := json.Unmarshal([]byte(creds["GCP_SERVICE_ACCOUNT_JSON"]), &sa); err != nil {
		return nil, err
	}
	return &gcloudClient{cfg: cfg, sa: sa, http: opts.client(), base: opts.BaseURL}, nil
}

// accessToken mints an oauth2 access token by JWT bearer assertion (RS256)
// against the SA token endpoint.
func (c *gcloudClient) accessToken(ctx context.Context) (string, error) {
	block, _ := pem.Decode([]byte(c.sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("gcloud: service account private_key does not parse")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("gcloud: service account key is not RSA")
	}
	now := time.Now()
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := encode(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + encode(map[string]any{
		"iss":   c.sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/ndev.clouddns.readwrite",
		"aud":   c.sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {input + "." + base64.RawURLEncoding.EncodeToString(sig)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gcloud token http %d: %w", resp.StatusCode, ErrAuth)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.AccessToken, nil
}

type gcloudRRSet struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Rrdatas []string `json:"rrdatas"`
}

func (c *gcloudClient) fqdn(rec Record) (string, error) {
	if _, err := c.cfg.RecordName(rec.Name); err != nil { // confinement check; gcloud wants the absolute name
		return "", err
	}
	return c.cfg.FQDN(rec.Name) + ".", nil
}

func (c *gcloudClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gcloud dns http %d", resp.StatusCode)
	}
	return raw, nil
}

func (c *gcloudClient) zonePath() string {
	return "/dns/v1/projects/" + c.cfg.Project + "/managedZones/" + c.cfg.ManagedZone
}

func (c *gcloudClient) change(ctx context.Context, adds, dels []gcloudRRSet) error {
	_, err := c.request(ctx, http.MethodPost, c.zonePath()+"/changes",
		map[string]any{"additions": adds, "deletions": dels})
	return err
}

func (c *gcloudClient) Ensure(ctx context.Context, rec Record) (Result, error) {
	fqdn, err := c.fqdn(rec)
	if err != nil {
		return Result{}, err
	}
	set := gcloudRRSet{Name: fqdn, Type: rec.Type, TTL: rec.TTL, Rrdatas: []string{rec.Data}}
	return Result{Record: rec, EffectiveTTL: rec.TTL}, c.change(ctx, []gcloudRRSet{set}, nil)
}

func (c *gcloudClient) Remove(ctx context.Context, rec Record) error {
	fqdn, err := c.fqdn(rec)
	if err != nil {
		return err
	}
	sets, err := c.sets(ctx)
	if err != nil {
		return err
	}
	var dels []gcloudRRSet
	for _, s := range sets {
		if s.Name == fqdn && s.Type == rec.Type {
			dels = append(dels, s)
		}
	}
	if len(dels) == 0 {
		return ErrNotFound
	}
	return c.change(ctx, nil, dels)
}

func (c *gcloudClient) sets(ctx context.Context) ([]gcloudRRSet, error) {
	raw, err := c.request(ctx, http.MethodGet, c.zonePath()+"/rrsets", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rrsets []gcloudRRSet `json:"rrsets"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Rrsets, nil
}

func (c *gcloudClient) List(ctx context.Context) ([]Record, error) {
	sets, err := c.sets(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, s := range sets {
		name := strings.TrimSuffix(strings.TrimSuffix(s.Name, "."), "."+c.cfg.Base)
		out = append(out, Record{Name: name, Type: s.Type, TTL: s.TTL, Data: strings.Join(s.Rrdatas, " ")})
	}
	return out, nil
}
