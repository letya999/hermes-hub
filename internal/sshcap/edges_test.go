package sshcap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestCertificateAuth(t *testing.T) {
	// A CA signs a user certificate over the fixture's client key; the sshd
	// accepts certs from that CA, and the service presents key+cert from
	// certificate_ref.
	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatal(err)
	}
	opts := sshdOpts{caKey: caSigner.PublicKey(), exec: func(cmd string) (string, string, int) { return "cert-ok", "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n    certificate_ref: \"file:keys/cert.pub\"\n", "")
	cert := &ssh.Certificate{
		Key:             f.keyPub,
		Serial:          1,
		CertType:        ssh.UserCert,
		KeyId:           "test-cert",
		ValidPrincipals: []string{"tester"},
		ValidAfter:      uint64(time.Now().Add(-time.Hour).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err = cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.cfgDir, "keys", "cert.pub"), ssh.MarshalAuthorizedKey(cert), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Exec(context.Background(), "box", "uptime")
	if err != nil || res.Stdout != "cert-ok" {
		t.Fatalf("cert auth: %v %+v", err, res)
	}

	// A certificate_ref that is a plain public key is rejected before dial.
	if err = os.WriteFile(filepath.Join(f.cfgDir, "keys", "cert.pub"), ssh.MarshalAuthorizedKey(f.keyPub), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrDenied) {
		t.Fatal("non-certificate certificate_ref accepted", err)
	}
}

func TestCredentialFileFailures(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return "", "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n", "")
	// Garbage key bytes reach the signer parse and fail.
	if err := os.WriteFile(filepath.Join(f.cfgDir, "keys", "id_ed25519"), []byte("not a key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unparseable key accepted", err)
	}
	// Missing key file fails the same closed way.
	if err := os.Remove(filepath.Join(f.cfgDir, "keys", "id_ed25519")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing key accepted", err)
	}
}

func TestFileReadTruncationAndBadPayload(t *testing.T) {
	fs := newMemFS()
	fs.files["/etc/app/big.txt"] = []byte(strings.Repeat("b", 500))
	extra := "    paths: [\"/etc/app/*\"]\n    write_paths: [\"/srv/app/*\"]\n    max_read_bytes: 64\n"
	f := openFixture(t, sshdOpts{fs: fs}, Grants{Write: true}, extra, "")
	got, err := f.svc.Read(context.Background(), "box", "/etc/app/big.txt")
	if err != nil || !got.Truncated || len(got.Text) != 64 {
		t.Fatalf("bounded read: %v %+v", err, got)
	}
	if _, err = f.svc.Write(context.Background(), "box", "/srv/app/x.txt", string([]byte{0xff, 0xfe})); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-UTF8 write accepted", err)
	}
}

func TestShellErrorPaths(t *testing.T) {
	opts := sshdOpts{shell: true}
	f := openFixture(t, opts, Grants{Shell: true}, "    commands: [\"uptime\"]\n", "")
	if _, err := f.svc.ShellOpen(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown alias", err)
	}
	view, err := f.svc.ShellOpen(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.ShellClose(view.ID)
	if err = f.svc.ShellSend(view.ID, strings.Repeat("x", 4097)); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized shell input", err)
	}
	f.svc.Close()
	if _, err = f.svc.ShellOpen(context.Background(), "box"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed service opened shell", err)
	}
	// PTY refused: server has no shell subsystem.
	noPty := openFixture(t, sshdOpts{}, Grants{Shell: true}, "    commands: [\"uptime\"]\n", "")
	if _, err = noPty.svc.ShellOpen(context.Background(), "box"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("pty refusal must surface as unavailable", err)
	}
}

func TestTunnelErrorPaths(t *testing.T) {
	opts := sshdOpts{directTCPIP: true}
	f := openFixture(t, opts, Grants{Tunnel: true}, "    commands: [\"uptime\"]\n    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 1}\n", "")
	if _, err := f.svc.TunnelOpen(context.Background(), "ghost", "db"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown alias", err)
	}
	f.svc.Close()
	if _, err := f.svc.TunnelOpen(context.Background(), "box", "db"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed service opened tunnel", err)
	}
	// Dead host: reserved port that is guaranteed closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	dir := t.TempDir()
	if err = os.MkdirAll(filepath.Join(dir, "keys"), 0700); err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := clientKeyPEM(t)
	if err = os.WriteFile(filepath.Join(dir, "keys", "id"), keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	body := "schema: 1\nhosts:\n  dead:\n    host: 127.0.0.1\n    port: " + deadPort +
		"\n    user: t\n    host_keys: [\"" + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))) + "\"]\n    key_ref: \"file:keys/id\"\n    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 1}\n"
	configPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(Options{ConfigPath: configPath, Grants: Grants{Tunnel: true},
		Auth: identity.Envelope{Schema: identity.Schema, PrincipalID: "me"}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err = svc.TunnelOpen(context.Background(), "dead", "db"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("dead host tunnel accepted", err)
	}
}

func TestSweepRemovesExpired(t *testing.T) {
	opts := sshdOpts{}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n",
		"defaults: {shell_idle_seconds: 5, shell_lifetime_seconds: 10, tunnel_idle_seconds: 5, tunnel_lifetime_seconds: 10, max_sessions: 2}\n")
	now := time.Now()
	f.svc.shells["old"] = &shell{id: "old", alias: "box", createdAt: now.Add(-time.Hour), lastUsed: now.Add(-time.Hour)}
	f.svc.shells["fresh"] = &shell{id: "fresh", alias: "box", createdAt: now, lastUsed: now}
	f.svc.tunnels["oldt"] = &tunnel{id: "oldt", alias: "box", created: now.Add(-time.Hour), lastUsed: now.Add(-time.Hour), lifetime: 10 * time.Second, idle: 5 * time.Second, listener: &nilListener{}, client: nil}
	f.svc.tunnels["newt"] = &tunnel{id: "newt", alias: "box", created: now, lastUsed: now, lifetime: time.Hour, idle: time.Hour, listener: &nilListener{}, client: nil}
	f.svc.sweep(now)
	if _, ok := f.svc.shells["old"]; ok {
		t.Fatal("expired shell not reaped")
	}
	if _, ok := f.svc.shells["fresh"]; !ok {
		t.Fatal("live shell reaped")
	}
	if _, ok := f.svc.tunnels["oldt"]; ok {
		t.Fatal("expired tunnel not reaped")
	}
	if _, ok := f.svc.tunnels["newt"]; !ok {
		t.Fatal("live tunnel reaped")
	}
}

// nilListener satisfies net.Listener for sweep-only tunnels that never accept.
type nilListener struct{}

func (l *nilListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *nilListener) Close() error              { return nil }
func (l *nilListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1")} }
