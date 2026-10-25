package sshcap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
)

const (
	keyFileMaxBytes = 64 << 10
	brokerBinding   = "ssh-"
	brokerWorkload  = "hermes-runtime"
)

// signers resolves the host credential reference and returns SSH auth
// signers plus a cleanup that must run once the dial attempt is done. Broker
// references acquire, materialize and release a lease inside this function;
// the plaintext key never reaches disk, env or chat.
func (s *Service) signers(ctx context.Context, alias string, h *Host) ([]ssh.Signer, func(), error) {
	raw, cleanup, err := s.resolveKey(ctx, alias, h, h.KeyRef, "SSH_PRIVATE_KEY")
	if err != nil {
		return nil, nil, err
	}
	defer clear(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("%w: host %q private key does not parse", ErrUnavailable, alias)
	}
	certSigner, err := s.withCertificate(ctx, alias, h, signer)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return []ssh.Signer{certSigner}, cleanup, nil
}

func (s *Service) withCertificate(ctx context.Context, alias string, h *Host, signer ssh.Signer) (ssh.Signer, error) {
	if h.CertificateRef == "" {
		return signer, nil
	}
	if strings.HasPrefix(h.CertificateRef, "ca:") {
		return s.mintCertificate(alias, h, signer)
	}
	raw, cleanup, err := s.resolveKey(ctx, alias, h, h.CertificateRef, "SSH_CERTIFICATE")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	defer clear(raw)
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q certificate does not parse", ErrUnavailable, alias)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%w: host %q certificate_ref is not an SSH certificate", ErrDenied, alias)
	}
	return ssh.NewCertSigner(cert, signer)
}

func (s *Service) resolveKey(ctx context.Context, alias string, h *Host, ref, envName string) ([]byte, func(), error) {
	scheme, rest, _ := strings.Cut(ref, ":")
	switch scheme {
	case "file":
		body, err := s.readCredentialFile(rest)
		return body, func() {}, err
	case "broker":
		return s.materializeKey(ctx, alias, rest, envName)
	}
	return nil, nil, fmt.Errorf("%w: host %q unknown credential scheme", ErrInvalid, alias)
}

// readCredentialFile reads an operator-placed key below the read-only config
// dir. os.Root keeps the path inside that directory on every platform.
func (s *Service) readCredentialFile(rel string) ([]byte, error) {
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%w: credential file %q: %v", ErrUnavailable, rel, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > keyFileMaxBytes {
		return nil, fmt.Errorf("%w: credential file %q must be a regular file", ErrInvalid, rel)
	}
	body, err := io.ReadAll(io.LimitReader(f, keyFileMaxBytes+1))
	if err != nil || len(body) > keyFileMaxBytes {
		return nil, fmt.Errorf("%w: credential file %q unreadable", ErrUnavailable, rel)
	}
	return body, nil
}

// materializeKey runs acquire -> materialize -> release against Credential
// Broker. The grant must match principal/context/runtime/policy and the
// "ssh:<alias>" binding; the released lease never lingers past the dial.
func (s *Service) materializeKey(ctx context.Context, alias, grantID, envName string) ([]byte, func(), error) {
	if !s.broker.Enabled() {
		return nil, nil, fmt.Errorf("%w: host %q broker credential but broker is not configured", ErrUnavailable, alias)
	}
	binding := brokerBinding + alias
	control, err := s.broker.NewForBinding(s.auth, "broker:control", binding, brokerWorkload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: broker control client: %v", ErrUnavailable, err)
	}
	runtime, err := s.broker.NewForBinding(s.auth, "broker:runtime", binding, brokerWorkload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: broker runtime client: %v", ErrUnavailable, err)
	}
	lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: grantID, TTLSeconds: 60})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: broker acquire: %v", ErrDenied, err)
	}
	release := func() {
		_ = runtime.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{})
	}
	materialized, err := runtime.Materialize(ctx, lease.ID)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("%w: broker materialize: %v", ErrUnavailable, err)
	}
	value := materialized.Env[envName]
	clear(materialized.Env)
	if value == "" {
		release()
		return nil, nil, fmt.Errorf("%w: broker lease did not deliver %s", ErrUnavailable, envName)
	}
	return []byte(value), release, nil
}

// mintCertificate signs the dial-time public key with an operator-placed CA
// key (certificate_ref "ca:<relative path>"). The CA key never leaves the
// read-only config dir, the image, chat or tool results; each minted cert is
// a fresh short-lived user certificate for the remote account — revocation
// is stopping issuance. Serial and principals land in the bounded SSH
// receipt; static file:/broker: certificate_ref stays the fallback.
const sshCertTTL = 10 * time.Minute

func (s *Service) mintCertificate(alias string, h *Host, signer ssh.Signer) (ssh.Signer, error) {
	raw, err := s.readCredentialFile(strings.TrimPrefix(h.CertificateRef, "ca:"))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	caSigner, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q ca key does not parse", ErrUnavailable, alias)
	}
	var serial [8]byte
	var nonce [32]byte
	if _, err := rand.Read(serial[:]); err != nil {
		return nil, fmt.Errorf("%w: host %q cert serial unavailable", ErrUnavailable, alias)
	}
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("%w: host %q cert nonce unavailable", ErrUnavailable, alias)
	}
	now := time.Now()
	cert := &ssh.Certificate{
		Key:             signer.PublicKey(),
		Serial:          binary.BigEndian.Uint64(serial[:]),
		CertType:        ssh.UserCert,
		KeyId:           "hub-" + alias,
		ValidPrincipals: []string{h.User},
		ValidAfter:      uint64(now.Add(-time.Minute).Unix()), // #nosec G115 -- wall clock is positive
		ValidBefore:     uint64(now.Add(sshCertTTL).Unix()),   // #nosec G115 -- wall clock is positive
		Nonce:           nonce[:],
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		return nil, fmt.Errorf("%w: host %q ca signing failed", ErrUnavailable, alias)
	}
	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q cert signer failed", ErrUnavailable, alias)
	}
	s.auditEvent("ssh-cert", alias, "ok", fmt.Sprintf("serial=%d principals=%s ttl=%ds", cert.Serial, strings.Join(cert.ValidPrincipals, ","), int(sshCertTTL/time.Second)))
	return certSigner, nil
}
