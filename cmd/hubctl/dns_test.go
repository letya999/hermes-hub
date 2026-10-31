package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/dns"
)

const dnsConfigYAML = `schema: 1
provider: digitalocean
zone: hub.example.com
credential_ref: "file:creds.json"
`

func writeDNSConfig(t *testing.T, dir, body string, creds map[string]string) {
	t.Helper()
	cfgDir := filepath.Join(dir, "connections", "dns")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if creds != nil {
		raw, _ := json.Marshal(creds)
		if err := os.WriteFile(filepath.Join(cfgDir, "creds.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeDO serves the DO records API for hubctl dns tests.
func fakeDO(t *testing.T) *httptest.Server {
	var records []map[string]any
	nextID := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			in["id"] = nextID
			nextID++
			records = append(records, in)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"domain_record": in})
		case "GET":
			json.NewEncoder(w).Encode(map[string]any{"domain_records": records})
		case "DELETE":
			id := 0
			fmt.Sscanf(r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:], "%d", &id)
			for i, x := range records {
				if x["id"] == id {
					records = append(records[:i], records[i+1:]...)
					w.WriteHeader(204)
					return
				}
			}
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The ACME account key is operator-owned state (R7): it is generated once
// and reloaded on later runs, stored 0600.
func TestACMEAccountKeyPersistence(t *testing.T) {
	dir := t.TempDir()
	k1, err := acmeAccountKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := acmeAccountKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	der1, _ := x509.MarshalECPrivateKey(k1)
	der2, _ := x509.MarshalECPrivateKey(k2)
	if string(der1) != string(der2) {
		t.Fatal("account key must be persisted, not regenerated per call")
	}
	info, err := os.Stat(filepath.Join(dir, "acme-account.key"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("account key mode: %o", info.Mode().Perm())
	}
}

func TestCLIDNSCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "check", "--dir", dir})
	})
	if !strings.Contains(out, "digitalocean") || !strings.Contains(out, "hub.example.com") {
		t.Fatalf("check output: %s", out)
	}
}

func TestCLIDNSCheckRegruFailClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, `schema: 1
provider: regru
zone: example.com
credential_ref: "file:creds.json"
`, map[string]string{"REGRU_USERNAME": "u"})
	err := run(ctx, []string{"dns", "check", "--dir", dir})
	if err == nil {
		t.Fatal("regru without static egress must fail closed")
	}
	if !strings.Contains(err.Error(), "static") || !strings.Contains(err.Error(), "delegat") {
		t.Fatalf("diagnostic must name both exits: %v", err)
	}
}

func TestCLIDNSEnsure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	do := fakeDO(t)
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", do.URL, "alice"})
	})
	if !strings.Contains(out, "*.alice") {
		t.Fatalf("ensure output: %s", out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "state", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"kind":"dns"`) {
		t.Fatalf("audit missing dns event: %s", body)
	}
}

// R4: the <user> positional must equal the zone label bound in the reviewed
// exposure config — one space's CLI cannot write another user's records.
func TestCLIDNSEnsureZoneBinding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir) // binds zone: alice
	do := fakeDO(t)
	err := run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", do.URL, "bob"})
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("cross-zone write must fail closed naming the bound zone, got: %v", err)
	}
	other := t.TempDir()
	writeDNSConfig(t, other, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	err = run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", other, "--endpoint", do.URL, "alice"})
	if err == nil {
		t.Fatal("unbound user zone (no exposure config) must fail closed")
	}
}

// Push obeys the same binding: a planted foreign cert must not be delivered.
func TestCLIDNSPushZoneBinding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, nil)
	writeExposureConfig(t, dir)
	certDir := filepath.Join(dir, "connections", "dns", "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"bob.crt.pem", "bob.key.pem"} {
		if err := os.WriteFile(filepath.Join(certDir, n), []byte("PEM"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := run(ctx, []string{"dns", "push", "--dir", dir, "--alias", "vps", "bob"})
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("cross-zone push must fail closed naming the bound zone, got: %v", err)
	}
}

// fakeACMEServer is a minimal ACME v2 directory/order flow that signs CSRs.
func fakeACMEServer(t *testing.T) *httptest.Server {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	var ok atomic.Bool
	var leaf atomic.Value
	var srv *httptest.Server
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Replay-Nonce", "n")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dir":
			reply(w, map[string]string{"newNonce": srv.URL + "/n", "newAccount": srv.URL + "/a", "newOrder": srv.URL + "/o"})
		case r.URL.Path == "/n":
			w.Header().Set("Replay-Nonce", "n")
		case r.URL.Path == "/a":
			w.Header().Set("Replay-Nonce", "n")
			w.Header().Set("Location", srv.URL+"/a/1")
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]any{"status": "valid"})
		case r.URL.Path == "/o":
			w.Header().Set("Location", srv.URL+"/o/1")
			w.WriteHeader(201)
			reply(w, map[string]any{"status": "pending", "authorizations": []string{srv.URL + "/z/1"}, "finalize": srv.URL + "/o/1/f"})
		case r.URL.Path == "/z/1":
			st := "pending"
			if ok.Load() {
				st = "valid"
			}
			reply(w, map[string]any{"status": st,
				"challenges": []map[string]any{{"type": "dns-01", "url": srv.URL + "/c/1", "token": "t", "status": st}}})
		case r.URL.Path == "/c/1":
			ok.Store(true)
			reply(w, map[string]any{"type": "dns-01", "status": "valid", "token": "t"})
		case r.URL.Path == "/o/1":
			if ok.Load() {
				reply(w, map[string]any{"status": "valid", "certificate": srv.URL + "/cert"})
			} else {
				reply(w, map[string]any{"status": "ready", "finalize": srv.URL + "/o/1/f"})
			}
		case r.URL.Path == "/o/1/f":
			var env struct {
				Payload string `json:"payload"`
			}
			json.NewDecoder(r.Body).Decode(&env)
			raw, _ := base64.RawURLEncoding.DecodeString(env.Payload)
			var in struct {
				CSR string `json:"csr"`
			}
			json.Unmarshal(raw, &in)
			csrDER, _ := base64.RawURLEncoding.DecodeString(in.CSR)
			csr, err := x509.ParseCertificateRequest(csrDER)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: csr.Subject, DNSNames: csr.DNSNames,
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
				KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, csr.PublicKey, caKey)
			leaf.Store(der)
			ok.Store(true)
			reply(w, map[string]any{"status": "valid", "certificate": srv.URL + "/cert"})
		case r.URL.Path == "/cert":
			pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: leaf.Load().([]byte)})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCLIDNSCert(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	do := fakeDO(t)
	acme := fakeACMEServer(t)
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "cert", "--dir", dir, "--endpoint", do.URL, "--acme", acme.URL + "/dir", "alice"})
	})
	if !strings.Contains(out, "alice") {
		t.Fatalf("cert output: %s", out)
	}
	cert, err := os.ReadFile(filepath.Join(dir, "connections", "dns", "certs", "alice.crt.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(cert)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(parsed.DNSNames) == 0 || parsed.DNSNames[0] != "*.alice.hub.example.com" {
		t.Fatalf("issued cert: %v %v", parsed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "connections", "dns", "certs", "alice.key.pem")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "state", "audit.jsonl"))
	if err != nil || !strings.Contains(string(body), `"kind":"dns"`) || !strings.Contains(string(body), "cert") {
		t.Fatalf("audit missing cert issuance event: %v %s", err, body)
	}
}

// Denials are the highest-value audit rows (R8): a refused cross-zone write
// leaves a dns event marking the denial.
func TestCLIDNSDenialAudited(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	do := fakeDO(t)
	_ = run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", do.URL, "bob"})
	body, err := os.ReadFile(filepath.Join(dir, "state", "audit.jsonl"))
	if err != nil {
		t.Fatal("denial must still open the ledger")
	}
	text := string(body)
	if !strings.Contains(text, `"kind":"dns"`) || !strings.Contains(text, "denied") {
		t.Fatalf("audit missing dns denial: %s", text)
	}
	if strings.Contains(text, "DIGITALOCEAN_TOKEN") || strings.Contains(text, "x\"") {
		t.Fatal("audit event leaked credential material")
	}
}

// R4 applies to issuance too: dns cert for another user's zone is denied
// before the ACME flow starts (no cert files appear).
func TestCLIDNSCertZoneBinding(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	do := fakeDO(t)
	acme := fakeACMEServer(t)
	err := run(ctx, []string{"dns", "cert", "--dir", dir, "--endpoint", do.URL, "--acme", acme.URL + "/dir", "bob"})
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("cross-zone cert must fail closed naming the bound zone, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "connections", "dns", "certs")); !os.IsNotExist(statErr) {
		t.Fatal("denied issuance must not create cert material")
	}
}

// A stored cert with plenty of validity left is not re-issued — renewal is
// due only inside the last 30 days (R7).
func TestCLIDNSCertSkipsFresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, nil)
	writeExposureConfig(t, dir)
	store := &dns.Store{Dir: filepath.Join(dir, "connections", "dns", "certs")}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(9), DNSNames: []string{"*.alice.hub.example.com"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(80 * 24 * time.Hour),
	}, &x509.Certificate{SerialNumber: big.NewInt(8), IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now(), NotAfter: time.Now().Add(80 * 24 * time.Hour)},
		&caKey.PublicKey, caKey)
	if err := store.Save("alice", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("k")})); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "cert", "--dir", dir, "alice"}) // no --acme: must not issue
	})
	if !strings.Contains(out, "valid until") {
		t.Fatalf("fresh cert should short-circuit issuance: %s", out)
	}
}

// R7: a failed renewal leaves the live cert untouched and raises an audit
// alert before expiry — nothing is silently deleted.
func TestCLIDNSCertRenewalFailureAlert(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	store := &dns.Store{Dir: filepath.Join(dir, "connections", "dns", "certs")}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now(), NotAfter: time.Now().Add(40 * 24 * time.Hour)}
	leaf, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(9), DNSNames: []string{"*.alice.hub.example.com"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(10 * 24 * time.Hour)}, tmpl, &caKey.PublicKey, caKey)
	good := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})
	if err := store.Save("alice", good, []byte("KEY")); err != nil {
		t.Fatal(err)
	}
	// ACME endpoint 404s — issuance fails while the old cert still has 10d.
	err := run(ctx, []string{"dns", "cert", "--dir", dir, "--endpoint", "http://127.0.0.1:1", "--acme", "http://127.0.0.1:1/dir", "alice"})
	if err == nil {
		t.Fatal("renewal against a dead ACME must fail")
	}
	kept, _, lerr := store.Load("alice")
	if lerr != nil || string(kept) != string(good) {
		t.Fatal("failed renewal must not disturb the live cert")
	}
	body, rerr := os.ReadFile(filepath.Join(dir, "state", "audit.jsonl"))
	if rerr != nil || !strings.Contains(string(body), `"kind":"dns"`) || !strings.Contains(string(body), "renew") {
		t.Fatalf("renewal failure must be alerted in the ledger: %v %s", rerr, body)
	}
}

func TestCLIDNSRecords(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "secret-token-9"})
	writeExposureConfig(t, dir)
	do := fakeDO(t)
	if err := run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", do.URL, "alice"}); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "records", "--dir", dir, "--endpoint", do.URL})
	})
	if !strings.Contains(out, "*.alice") || strings.Contains(out, "secret-token-9") {
		t.Fatalf("records output: %s", out)
	}
}

// Offboarding tears the wildcard record back down through the same bound
// zone and leaves an audit row.
func TestCLIDNSRemove(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, map[string]string{"DIGITALOCEAN_TOKEN": "x"})
	writeExposureConfig(t, dir)
	srv := fakeDO(t)
	if err := run(ctx, []string{"dns", "ensure", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", srv.URL, "alice"}); err != nil {
		t.Fatal(err)
	}
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "remove", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", srv.URL, "alice"})
	})
	if !strings.Contains(out, "removed") {
		t.Fatalf("remove output: %s", out)
	}
	out = captureOutput(t, func() error {
		return run(ctx, []string{"dns", "records", "--dir", dir, "--endpoint", srv.URL})
	})
	if strings.Contains(out, "alice") {
		t.Fatalf("record still listed after remove: %s", out)
	}
	if err := run(ctx, []string{"dns", "remove", "--ip", "203.0.113.7", "--dir", dir, "--endpoint", srv.URL, "bob"}); err == nil {
		t.Fatal("cross-zone remove must fail closed")
	}
	body, err := os.ReadFile(filepath.Join(dir, "state", "audit.jsonl"))
	if err != nil || !strings.Contains(string(body), "remove") {
		t.Fatalf("zone delete must be audited: %v %s", err, body)
	}
}

func TestCLIDNSDelegateCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, `schema: 1
provider: digitalocean
zone: example.com
base: hub.example.com
credential_ref: "file:creds.json"
name_servers: [ns1.digitalocean.com]
`, nil)
	orig := dns.LookupNS
	t.Cleanup(func() { dns.LookupNS = orig })
	dns.LookupNS = func(_ context.Context, zone string) ([]string, error) {
		if zone != "hub.example.com" {
			t.Fatalf("looked up %s, want hub.example.com", zone)
		}
		return []string{"ns1.digitalocean.com."}, nil
	}
	out := captureOutput(t, func() error {
		return run(ctx, []string{"dns", "delegate-check", "--dir", dir})
	})
	if !strings.Contains(out, "hub.example.com") {
		t.Fatalf("delegate-check output: %s", out)
	}
	dns.LookupNS = func(context.Context, string) ([]string, error) { return []string{"ns1.reg.ru."}, nil }
	if err := run(ctx, []string{"dns", "delegate-check", "--dir", dir}); err == nil {
		t.Fatal("mismatched delegation must fail")
	}
}

func TestCLIDNSPushFailsClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeDNSConfig(t, dir, dnsConfigYAML, nil)
	writeExposureConfig(t, dir)
	// No issued cert and no ssh config — push must fail closed on the cert
	// lookup before any network or SSH work.
	err := run(ctx, []string{"dns", "push", "--dir", dir, "--alias", "vps", "alice"})
	if err == nil {
		t.Fatal("push without issued cert must fail")
	}
	if !strings.Contains(err.Error(), "alice.crt.pem") {
		t.Fatalf("push must fail at cert load, got: %v", err)
	}
	certDir := filepath.Join(dir, "connections", "dns", "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alice.crt.pem", "alice.key.pem"} {
		if err := os.WriteFile(filepath.Join(certDir, n), []byte("PEM"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err = run(ctx, []string{"dns", "push", "--dir", dir, "--alias", "vps", "alice"})
	if err == nil || !strings.Contains(err.Error(), filepath.Join("connections", "ssh")) {
		t.Fatalf("with cert present push must fail at ssh layer, got: %v", err)
	}
	// With a valid ssh config the push reaches the wire: dial failure proves
	// the write path runs (no sshd listens on 127.0.0.1:1).
	sshDir := filepath.Join(dir, "connections", "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshCfg := `schema: 1
hosts:
  vps:
    host: 127.0.0.1
    port: 1
    user: op
    host_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX"]
    key_ref: "file:dummy.pem"
    write_paths: ["/etc/sish/certs/*"]
`
	if err := os.WriteFile(filepath.Join(sshDir, "config.yaml"), []byte(sshCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "dummy.pem"), []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(ctx, []string{"dns", "push", "--dir", dir, "--alias", "vps", "alice"})
	if err == nil || strings.Contains(err.Error(), "not configured") {
		t.Fatalf("push must attempt ssh delivery, got: %v", err)
	}
}
