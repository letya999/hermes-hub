package dns

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The shared provider contract (SPEC-0076 R1/R2): Ensure upserts relative
// names into the configured zone, Remove deletes, List reads back, and
// provider errors map onto the typed taxonomy. Each profile gets an
// httptest fake speaking its real wire format.
func TestProviderContract(t *testing.T) {
	providers := map[string]func(t *testing.T) (*Config, map[string]string, Options){
		"digitalocean": fakeDigitalOcean,
		"regru":        fakeRegru,
		"gcloud":       fakeGcloud,
	}
	for name, mk := range providers {
		t.Run(name, func(t *testing.T) {
			cfg, creds, opts := mk(t)
			p, err := cfg.NewProvider(creds, opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()

			res, err := p.Ensure(ctx, Record{Name: "*.alice", Type: "A", Data: "203.0.113.7", TTL: 300})
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if res.Record.Name != "*.alice" {
				t.Fatalf("result echoes wrong record: %+v", res)
			}
			recs, err := p.List(ctx)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			found := false
			for _, r := range recs {
				if r.Type == "A" && strings.Contains(r.Name, "alice") && r.Data == "203.0.113.7" {
					found = true
				}
			}
			if !found {
				t.Fatalf("list did not read back the ensured record: %+v", recs)
			}
			if err := p.Remove(ctx, Record{Name: "*.alice", Type: "A", Data: "203.0.113.7"}); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := p.Ensure(ctx, Record{Name: "_acme-challenge.alice", Type: "TXT", Data: "tok123", TTL: 300}); err != nil {
				t.Fatalf("ensure txt: %v", err)
			}
			recs, err = p.List(ctx)
			if err != nil {
				t.Fatalf("list after txt: %v", err)
			}
			found = false
			for _, r := range recs {
				if r.Type == "TXT" && strings.Contains(r.Name, "_acme-challenge.alice") && strings.Contains(r.Data, "tok123") {
					found = true
				}
			}
			if !found {
				t.Fatalf("list did not read back the txt record: %+v", recs)
			}
			if err := p.Remove(ctx, Record{Name: "_acme-challenge.alice", Type: "TXT", Data: "tok123"}); err != nil {
				t.Fatalf("remove txt: %v", err)
			}
			if _, err := p.Ensure(ctx, Record{Name: "*.alice", Type: "AAAA", Data: "2001:db8::7", TTL: 300}); err != nil {
				t.Fatalf("ensure aaaa: %v", err)
			}
			recs, err = p.List(ctx)
			if err != nil {
				t.Fatalf("list after aaaa: %v", err)
			}
			found = false
			for _, r := range recs {
				if r.Type == "AAAA" && strings.Contains(r.Name, "alice") && r.Data == "2001:db8::7" {
					found = true
				}
			}
			if !found {
				t.Fatalf("list did not read back the aaaa record: %+v", recs)
			}
			if err := p.Remove(ctx, Record{Name: "*.alice", Type: "AAAA", Data: "2001:db8::7"}); err != nil {
				t.Fatalf("remove aaaa: %v", err)
			}
			if err := p.Remove(ctx, Record{Name: "*.ghost", Type: "A", Data: "9.9.9.9"}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("remove absent: want ErrNotFound, got %v", err)
			}
		})
	}
}

// fakeDigitalOcean serves /v2/domains/{zone}/records per the DO API.
func fakeDigitalOcean(t *testing.T) (*Config, map[string]string, Options) {
	type rec struct {
		ID   int    `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
		Data string `json:"data"`
		TTL  int    `json:"ttl"`
	}
	var records []rec
	nextID := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer do-token" {
			w.WriteHeader(401)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v2/domains/hub.example.com/records") {
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(map[string]any{"domain_records": records})
		case "POST":
			var in rec
			json.NewDecoder(r.Body).Decode(&in)
			in.ID = nextID
			nextID++
			records = append(records, in)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"domain_record": in})
		case "DELETE":
			id := 0
			fmt.Sscanf(r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:], "%d", &id)
			for i, x := range records {
				if x.ID == id {
					records = append(records[:i], records[i+1:]...)
					w.WriteHeader(204)
					return
				}
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &Config{Provider: "digitalocean", Zone: "hub.example.com", Base: "hub.example.com"}
	return cfg, map[string]string{"DIGITALOCEAN_TOKEN": "do-token"}, Options{BaseURL: srv.URL, HTTPClient: srv.Client()}
}

// fakeRegru serves POST /api/regru2/zone/* with form credentials and a
// JSON {result, answer} envelope, per REG.API v2.
func fakeRegru(t *testing.T) (*Config, map[string]string, Options) {
	type rec struct {
		Subname string `json:"subname"`
		Rectype string `json:"rectype"`
		Content string `json:"content"`
		TTL     int    `json:"ttl,omitempty"`
	}
	records := []rec{{Subname: "@", Rectype: "SOA", Content: "ns1.reg.ru. hostmaster.reg.ru.", TTL: 3600}}
	reply := func(w http.ResponseWriter, answer any, code string) {
		if code == "" {
			json.NewEncoder(w).Encode(map[string]any{"result": "success", "answer": answer})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": "error", "error_code": code})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("username") != "u" || r.Form.Get("password") != "p" {
			reply(w, nil, "AUTHENTICATION_FAILED")
			return
		}
		var input map[string]any
		json.Unmarshal([]byte(r.Form.Get("input_data")), &input)
		sub, _ := input["subdomain"].(string)
		switch {
		case strings.HasSuffix(r.URL.Path, "/zone/add_alias"):
			records = append(records, rec{Subname: sub, Rectype: "A", Content: input["ipaddr"].(string)})
			reply(w, map[string]any{"domains": []any{}}, "")
		case strings.HasSuffix(r.URL.Path, "/zone/add_txt"):
			records = append(records, rec{Subname: sub, Rectype: "TXT", Content: input["text"].(string)})
			reply(w, map[string]any{"domains": []any{}}, "")
		case strings.HasSuffix(r.URL.Path, "/zone/add_aaaa"):
			records = append(records, rec{Subname: sub, Rectype: "AAAA", Content: input["ipaddr"].(string)})
			reply(w, map[string]any{"domains": []any{}}, "")
		case strings.HasSuffix(r.URL.Path, "/zone/get_resource_records"):
			reply(w, map[string]any{"domains": []any{map[string]any{"dname": "example.com", "rrs": records}}}, "")
		case strings.HasSuffix(r.URL.Path, "/zone/remove_record"):
			for i, x := range records {
				if x.Subname == sub && x.Rectype == input["record_type"] && x.Content == input["content"] {
					records = append(records[:i], records[i+1:]...)
					reply(w, map[string]any{"domains": []any{}}, "")
					return
				}
			}
			reply(w, nil, "RECORD_NOT_FOUND")
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &Config{Provider: "regru", Zone: "example.com", Base: "hub.example.com"}
	return cfg, map[string]string{"REGRU_USERNAME": "u", "REGRU_PASSWORD": "p"}, Options{BaseURL: srv.URL + "/api/regru2/", HTTPClient: srv.Client()}
}

// fakeGcloud serves the oauth2 SA token exchange plus the Cloud DNS
// managedZones changes/rrsets surface. The token endpoint verifies the JWT
// assertion's RS256 signature and claims against the SA public key.
func fakeGcloud(t *testing.T) (*Config, map[string]string, Options) {
	type rrset struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		TTL     int      `json:"ttl"`
		Rrdatas []string `json:"rrdatas"`
	}
	saKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var sets []rrset
	var srv *httptest.Server
	verifyJWT := func(assertion string) bool {
		parts := strings.Split(assertion, ".")
		if len(parts) != 3 {
			return false
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return false
		}
		var claims struct {
			ISS   string `json:"iss"`
			Scope string `json:"scope"`
			AUD   string `json:"aud"`
		}
		if json.Unmarshal(payload, &claims) != nil {
			return false
		}
		if claims.ISS != "dns@my-proj.iam.gserviceaccount.com" || !strings.Contains(claims.Scope, "ndev.clouddns") || !strings.HasPrefix(claims.AUD, srvURL(srv)) {
			return false
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return false
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		return rsa.VerifyPKCS1v15(&saKey.PublicKey, crypto.SHA256, digest[:], sig) == nil
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || !verifyJWT(r.Form.Get("assertion")) {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.fake", "token_type": "Bearer", "expires_in": 3600})
		case strings.HasSuffix(r.URL.Path, "/rrsets"):
			if r.Header.Get("Authorization") != "Bearer ya29.fake" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"rrsets": sets})
		case strings.HasSuffix(r.URL.Path, "/changes"):
			var ch struct {
				Additions []rrset `json:"additions"`
				Deletions []rrset `json:"deletions"`
			}
			json.NewDecoder(r.Body).Decode(&ch)
			sets = append(sets, ch.Additions...)
			for _, del := range ch.Deletions {
				for i, x := range sets {
					if x.Name == del.Name && x.Type == del.Type {
						sets = append(sets[:i], sets[i+1:]...)
					}
				}
			}
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]any{"status": "done"})
		}
	}))
	t.Cleanup(srv.Close)
	der, err := x509.MarshalPKCS8PrivateKey(saKey)
	if err != nil {
		t.Fatal(err)
	}
	sa := map[string]any{
		"client_email": "dns@my-proj.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    srv.URL + "/token",
	}
	raw, _ := json.Marshal(sa)
	cfg := &Config{Provider: "gcloud", Zone: "hub.example.com", Base: "hub.example.com", Project: "my-proj", ManagedZone: "hub-zone"}
	return cfg, map[string]string{"GCP_SERVICE_ACCOUNT_JSON": string(raw)}, Options{BaseURL: srv.URL, HTTPClient: srv.Client()}
}

// srvURL reads a test server's address inside handler callbacks where the
// variable is captured before assignment.
func srvURL(s *httptest.Server) string {
	if s == nil {
		return ""
	}
	return s.URL
}

// Zone confinement inside the provider methods (R4): an invalid or
// escaping record name is refused locally — no HTTP request is sent.
func TestProviderRejectsBadNames(t *testing.T) {
	providers := map[string]func(t *testing.T) (*Config, map[string]string, Options){
		"digitalocean": fakeDigitalOcean,
		"regru":        fakeRegru,
		"gcloud":       fakeGcloud,
	}
	for name, mk := range providers {
		t.Run(name, func(t *testing.T) {
			cfg, creds, opts := mk(t)
			p, err := cfg.NewProvider(creds, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"UPPER.alice", "x..alice", "-bad.alice", "trailing-.alice"} {
				if _, err := p.Ensure(context.Background(), Record{Name: bad, Type: "A", Data: "1.2.3.4"}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("name %q: want ErrInvalid, got %v", bad, err)
				}
			}
		})
	}
}

// reg.ru has no per-record TTL — R1 requires EffectiveTTL to report the
// zone SOA TTL instead. The fake seeds an SOA rr with ttl 3600.
func TestRegruEffectiveTTLFromSOA(t *testing.T) {
	cfg, creds, opts := fakeRegru(t)
	p, err := cfg.NewProvider(creds, opts)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Ensure(context.Background(), Record{Name: "*.alice", Type: "A", Data: "203.0.113.7", TTL: 300})
	if err != nil {
		t.Fatal(err)
	}
	if res.EffectiveTTL != 3600 {
		t.Fatalf("EffectiveTTL must come from the zone SOA, got %d", res.EffectiveTTL)
	}
}

// Provider auth failures map onto ErrAuth so callers can tell credential
// problems from transient ones (SPEC-0076 R8 sanitized taxonomy).
func TestProviderErrorTaxonomy(t *testing.T) {
	bad := map[string]func(t *testing.T) (*Config, map[string]string, Options){
		"digitalocean": func(t *testing.T) (*Config, map[string]string, Options) {
			cfg, _, opts := fakeDigitalOcean(t)
			return cfg, map[string]string{"DIGITALOCEAN_TOKEN": "wrong"}, opts
		},
		"regru": func(t *testing.T) (*Config, map[string]string, Options) {
			cfg, _, opts := fakeRegru(t)
			return cfg, map[string]string{"REGRU_USERNAME": "u", "REGRU_PASSWORD": "wrong"}, opts
		},
		"gcloud": func(t *testing.T) (*Config, map[string]string, Options) {
			cfg, creds, opts := fakeGcloud(t)
			var sa map[string]any
			json.Unmarshal([]byte(creds["GCP_SERVICE_ACCOUNT_JSON"]), &sa)
			other, _ := rsa.GenerateKey(rand.Reader, 2048)
			der, _ := x509.MarshalPKCS8PrivateKey(other)
			sa["private_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
			raw, _ := json.Marshal(sa)
			return cfg, map[string]string{"GCP_SERVICE_ACCOUNT_JSON": string(raw)}, opts
		},
	}
	for name, mk := range bad {
		t.Run(name, func(t *testing.T) {
			cfg, creds, opts := mk(t)
			p, err := cfg.NewProvider(creds, opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Ensure(context.Background(), Record{Name: "*.alice", Type: "A", Data: "203.0.113.7", TTL: 300})
			if !errors.Is(err, ErrAuth) {
				t.Fatalf("want ErrAuth, got %v", err)
			}
			if _, err := p.List(context.Background()); !errors.Is(err, ErrAuth) {
				t.Fatalf("list: want ErrAuth, got %v", err)
			}
		})
	}
}

// R8 bounds: a provider call must not hang forever — Options.Timeout caps
// the whole HTTP exchange.
func TestProviderCallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	cfg := &Config{Provider: "digitalocean", Zone: "hub.example.com", Base: "hub.example.com"}
	p, err := cfg.NewProvider(map[string]string{"DIGITALOCEAN_TOKEN": "t"}, Options{BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Ensure(context.Background(), Record{Name: "*.a", Type: "A", Data: "1.2.3.4", TTL: 300})
	if err == nil {
		t.Fatal("hanging provider must error")
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("call must respect Options.Timeout, took %s", time.Since(start))
	}
}

func TestProviderDefaultsHTTPClient(t *testing.T) {
	for _, tc := range []struct {
		cfg   *Config
		creds map[string]string
	}{
		{&Config{Provider: "digitalocean", Zone: "z", Base: "z"}, map[string]string{}},
		{&Config{Provider: "regru", Zone: "z", Base: "z"}, map[string]string{}},
		{&Config{Provider: "gcloud", Zone: "z", Base: "z", ManagedZone: "m"}, map[string]string{"GCP_SERVICE_ACCOUNT_JSON": `{"client_email":"a","private_key":"k","token_uri":"u"}`}},
	} {
		p, err := tc.cfg.NewProvider(tc.creds, Options{})
		if err != nil {
			t.Fatalf("%s: %v", tc.cfg.Provider, err)
		}
		var c *http.Client
		switch v := p.(type) {
		case *doClient:
			c = v.http
		case *regruClient:
			c = v.http
		case *gcloudClient:
			c = v.http
		}
		if c == nil {
			t.Fatalf("%s: nil http client", tc.cfg.Provider)
		}
	}
}
