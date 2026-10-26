package dns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// Store keeps issued cert/key pairs as 0600 files under a control-home
// directory (SPEC-0078 R7).
type Store struct{ Dir string }

func (s *Store) paths(user string) (cert, key string) {
	return filepath.Join(s.Dir, user+".crt.pem"), filepath.Join(s.Dir, user+".key.pem")
}

func (s *Store) Save(user string, cert, key []byte) error {
	cp, kp := s.paths(user)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cp, cert, 0o600); err != nil {
		return err
	}
	return os.WriteFile(kp, key, 0o600)
}

// RemoteWriter writes a text file on the sish host over the enrolment SSH
// channel — satisfied by sshcap.Service.Write at the call site.
type RemoteWriter func(ctx context.Context, alias, remotePath, text string) error

// PushCert drops <user>.{crt,key}.pem into the sish --https-certificate-directory.
func PushCert(ctx context.Context, w RemoteWriter, alias, dir, user string, cert, key []byte) error {
	base := strings.TrimSuffix(dir, "/")
	if err := w(ctx, alias, base+"/"+user+".crt.pem", string(cert)); err != nil {
		return err
	}
	return w(ctx, alias, base+"/"+user+".key.pem", string(key))
}

func (s *Store) Load(user string) (cert, key []byte, err error) {
	cp, kp := s.paths(user)
	if cert, err = os.ReadFile(cp); err != nil {
		return nil, nil, err
	}
	if key, err = os.ReadFile(kp); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// Expiry reports the stored leaf certificate's NotAfter — the renewal clock
// (R7). Missing or unparsable material surfaces as an error.
func (s *Store) Expiry(user string) (time.Time, error) {
	cert, _, err := s.Load(user)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return time.Time{}, fmt.Errorf("%w: %s cert is not PEM", ErrInvalid, user)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}

// Issuer runs ACME DNS-01 for per-user wildcard certificates through the
// configured record provider (SPEC-0078 R5).
type Issuer struct {
	Dir      string
	Key      *ecdsa.PrivateKey
	Provider Provider
	Config   *Config
}

// IssueWildcard issues *.<user>.<base> and returns PEM cert chain and key.
func (i *Issuer) IssueWildcard(ctx context.Context, user string) (certPEM, keyPEM []byte, err error) {
	client := &acme.Client{Key: i.Key, DirectoryURL: i.Dir, UserAgent: "hermes-hub-dns"}
	domain := "*." + user + "." + i.Config.Base
	order, err := client.AuthorizeOrder(ctx, []acme.AuthzID{{Type: "dns", Value: domain}})
	if err != nil {
		return nil, nil, err
	}
	var chal *acme.Challenge
	var authzURL string
	for _, u := range order.AuthzURLs {
		a, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range a.Challenges {
			if c.Type == "dns-01" {
				chal, authzURL = c, u
			}
		}
	}
	txtVal, err := client.DNS01ChallengeRecord(chal.Token)
	if err != nil {
		return nil, nil, err
	}
	txt := Record{Name: "_acme-challenge." + user, Type: "TXT", Data: txtVal, TTL: 300}
	if _, err = i.Provider.Ensure(ctx, txt); err != nil {
		return nil, nil, err
	}
	defer i.Provider.Remove(context.WithoutCancel(ctx), txt)
	if _, err = client.Accept(ctx, chal); err != nil {
		return nil, nil, err
	}
	if _, err = client.WaitAuthorization(ctx, authzURL); err != nil {
		return nil, nil, err
	}
	domKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domain},
		DNSNames: []string{domain},
	}, domKey)
	if err != nil {
		return nil, nil, err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(domKey)
	if err != nil {
		return nil, nil, err
	}
	var chain bytes.Buffer
	for _, d := range der {
		if err := pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: d}); err != nil {
			return nil, nil, err
		}
	}
	return chain.Bytes(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}
