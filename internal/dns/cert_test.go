package dns

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
	"errors"
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
)

// fakeACME serves the ACME v2 directory/order/authz/challenge/finalize/cert
// surface over httptest and signs finalization CSRs with a throwaway CA.
func fakeACME(t *testing.T) *httptest.Server {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake-acme-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	var authzValid atomic.Bool
	var finalized atomic.Bool
	var leafDER atomic.Value
	var srv *httptest.Server
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Replay-Nonce", "n")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dir":
			j(w, map[string]string{
				"newNonce":   srv.URL + "/nonce",
				"newAccount": srv.URL + "/acct",
				"newOrder":   srv.URL + "/order",
			})
		case r.URL.Path == "/nonce":
			w.Header().Set("Replay-Nonce", "n")
		case r.URL.Path == "/acct":
			payload := jwsPayload(t, r)
			w.Header().Set("Replay-Nonce", "n")
			w.Header().Set("Location", srv.URL+"/acct/1")
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(payload, "onlyReturnExisting") {
				w.WriteHeader(200)
			} else {
				w.WriteHeader(201)
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "valid"})
		case r.URL.Path == "/order":
			w.Header().Set("Location", srv.URL+"/order/1")
			w.WriteHeader(201)
			j(w, map[string]any{
				"status":         "pending",
				"authorizations": []string{srv.URL + "/authz/1"},
				"finalize":       srv.URL + "/order/1/finalize",
			})
		case r.URL.Path == "/authz/1":
			status := "pending"
			if authzValid.Load() {
				status = "valid"
			}
			j(w, map[string]any{
				"status":     status,
				"identifier": map[string]string{"type": "dns", "value": "*.alice.hub.example.com"},
				"challenges": []map[string]any{{
					"type":   "dns-01",
					"url":    srv.URL + "/chal/1",
					"token":  "tok",
					"status": status,
				}},
			})
		case r.URL.Path == "/chal/1":
			authzValid.Store(true)
			j(w, map[string]any{"type": "dns-01", "status": "valid", "token": "tok"})
		case r.URL.Path == "/order/1":
			if finalized.Load() {
				j(w, map[string]any{"status": "valid", "certificate": srv.URL + "/cert/1"})
			} else {
				j(w, map[string]any{"status": "ready", "finalize": srv.URL + "/order/1/finalize"})
			}
		case r.URL.Path == "/order/1/finalize":
			var in struct {
				CSR string `json:"csr"`
			}
			json.Unmarshal([]byte(jwsPayload(t, r)), &in)
			csrDER, err := base64.RawURLEncoding.DecodeString(in.CSR)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			csr, err := x509.ParseCertificateRequest(csrDER)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			leaf := &x509.Certificate{
				SerialNumber: big.NewInt(2),
				Subject:      csr.Subject,
				NotBefore:    time.Now().Add(-time.Hour),
				NotAfter:     time.Now().Add(90 * 24 * time.Hour),
				DNSNames:     csr.DNSNames,
				KeyUsage:     x509.KeyUsageDigitalSignature,
				ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			der, err := x509.CreateCertificate(rand.Reader, leaf, ca, csr.PublicKey, caKey)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			leafDER.Store(der)
			finalized.Store(true)
			j(w, map[string]any{"status": "valid", "certificate": srv.URL + "/cert/1"})
		case r.URL.Path == "/cert/1":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER.Load().([]byte)})
			pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: caDER})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jwsPayload unwraps the base64url payload of an ACME JWS request.
func jwsPayload(t *testing.T, r *http.Request) string {
	t.Helper()
	var env struct {
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestIssuerIssueWildcard(t *testing.T) {
	acme := fakeACME(t)
	var txts []Record
	prov := &stubProvider{ensure: func(rec Record) { txts = append(txts, rec) },
		remove: func(rec Record) {
			for i, x := range txts {
				if x.Name == rec.Name && x.Data == rec.Data {
					txts = append(txts[:i], txts[i+1:]...)
				}
			}
		}}
	acct, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	iss := &Issuer{
		Dir:      acme.URL + "/dir",
		Key:      acct,
		Provider: prov,
		Config:   &Config{Provider: "x", Zone: "hub.example.com", Base: "hub.example.com"},
	}
	cert, key, err := iss.IssueWildcard(context.Background(), "alice")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		t.Fatal("cert is not pem")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "*.alice.hub.example.com" {
		t.Fatalf("cert sans: %v", parsed.DNSNames)
	}
	if len(key) == 0 {
		t.Fatal("no key material")
	}
	for _, r := range txts {
		if !strings.HasPrefix(r.Name, "_acme-challenge.") {
			t.Fatalf("unexpected record touched: %+v", r)
		}
	}
	if len(txts) != 0 {
		t.Fatalf("challenge txt not cleaned up: %+v", txts)
	}
}

type stubProvider struct {
	ensure func(Record)
	remove func(Record)
}

func (s *stubProvider) Ensure(_ context.Context, rec Record) (Result, error) {
	s.ensure(rec)
	return Result{Record: rec}, nil
}
func (s *stubProvider) Remove(_ context.Context, rec Record) error {
	if s.remove != nil {
		s.remove(rec)
	}
	return nil
}
func (s *stubProvider) List(context.Context) ([]Record, error) { return nil, nil }

func TestCertStorePerms(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	if err := st.Save("alice", []byte("CERT"), []byte("KEY")); err != nil {
		t.Fatal(err)
	}
	cert, key, err := st.Load("alice")
	if err != nil {
		t.Fatal(err)
	}
	if string(cert) != "CERT" || string(key) != "KEY" {
		t.Fatal("roundtrip mismatch")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"alice.crt.pem", "alice.key.pem"} {
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s mode %o leaks group/other", name, fi.Mode().Perm())
			}
		}
	}
}

// Save fails closed when the store dir cannot be created (path collision).
func TestCertStoreSaveFailsOnBadDir(t *testing.T) {
	dir := t.TempDir()
	clash := filepath.Join(dir, "certs")
	if err := os.WriteFile(clash, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Store{Dir: clash}).Save("alice", []byte("C"), []byte("K")); err == nil {
		t.Fatal("save into a file-occupied dir must fail")
	}
}

// Expiry is the renewal clock (R7): it returns the stored leaf's NotAfter
// and errors on missing or non-PEM material.
func TestStoreExpiry(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	if _, err := st.Expiry("ghost"); err == nil {
		t.Fatal("missing cert must error")
	}
	if err := st.Save("bad", []byte("not pem"), []byte("k")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Expiry("bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-PEM cert: %v", err)
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: time.Now(), NotAfter: time.Now().Add(24 * time.Hour)}
	want := time.Now().Add(72 * time.Hour)
	leaf, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), NotAfter: want, NotBefore: time.Now()},
		tmpl, &caKey.PublicKey, caKey)
	if err := st.Save("alice", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}), []byte("k")); err != nil {
		t.Fatal(err)
	}
	got, err := st.Expiry("alice")
	if err != nil || !got.Equal(want.Truncate(time.Second)) {
		t.Fatalf("expiry: %v %v", got, err)
	}
}

func TestPushCert(t *testing.T) {
	writes := map[string]string{}
	w := func(_ context.Context, alias, path, text string) error {
		if alias != "vps" {
			t.Fatalf("alias: %s", alias)
		}
		writes[path] = text
		return nil
	}
	err := PushCert(context.Background(), RemoteWriter(w), "vps", "/etc/sish/certs", "alice", []byte("CERT"), []byte("KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if writes["/etc/sish/certs/alice.crt.pem"] != "CERT" || writes["/etc/sish/certs/alice.key.pem"] != "KEY" {
		t.Fatalf("writes: %+v", writes)
	}
}
