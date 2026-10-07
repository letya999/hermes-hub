package sshcap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

// clientKeyPEM returns an OpenSSH private key block and its public key.
func clientKeyPEM(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), signer.PublicKey()
}

type fixture struct {
	svc    *Service
	audit  string
	keyPub ssh.PublicKey
	cfgDir string
}

// openFixture writes config.yaml + keys/id_ed25519 and opens the service.
// hostBody customizes the host stanza after the credential lines.
func openFixture(t *testing.T, opts sshdOpts, grants Grants, hostExtra, defaults string) *fixture {
	t.Helper()
	keyPEM, keyPub := clientKeyPEM(t)
	opts.authKey = keyPub
	d := startSSHD(t, opts)
	parts := strings.Split(d.addr, ":")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "id_ed25519"), keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(d.hostKey)))
	body := "schema: 1\n" + defaults +
		"hosts:\n  box:\n    host: 127.0.0.1\n    port: " + parts[len(parts)-1] +
		"\n    user: tester\n    host_keys: [\"" + hostKey + "\"]\n    key_ref: \"file:keys/id_ed25519\"\n" + hostExtra
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	svc, err := Open(Options{ConfigPath: configPath, Grants: grants,
		Auth:      identity.Envelope{Schema: identity.Schema, PrincipalID: "me", ContextID: "me", RuntimeID: "test-runtime", PolicyVersion: "p1"},
		AuditPath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &fixture{svc: svc, audit: auditPath, keyPub: keyPub, cfgDir: dir}
}

func (f *fixture) auditEvents(t *testing.T) []audit.Event {
	t.Helper()
	ledger, err := audit.Open(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("me")
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func hasDenied(events []audit.Event, needle string) bool {
	for _, e := range events {
		if e.Outcome == "denied" && strings.Contains(e.Receipt, needle) {
			return true
		}
	}
	return false
}

func TestExecEndToEnd(t *testing.T) {
	seen := make(chan string, 8)
	opts := sshdOpts{exec: func(cmd string) (string, string, int) {
		seen <- cmd
		return "out:" + cmd, "warn", 0
	}}
	f := openFixture(t, opts, Grants{},
		"    commands: [\"uptime\", \"check\"]\n    sudo: passwordless\n", "")
	res, err := f.svc.Exec(context.Background(), "box", "uptime")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "out:uptime" || res.Stderr != "warn" || res.ExitCode != 0 || res.Write {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := <-seen; got != "uptime" {
		t.Fatal("server saw", got)
	}
	if _, err = f.svc.Exec(context.Background(), "box", "sudo check"); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "sudo -n check" {
		t.Fatal("passwordless sudo must become sudo -n, server saw", got)
	}
	if _, err = f.svc.Exec(context.Background(), "box", "rm -rf /"); !errors.Is(err, ErrDenied) {
		t.Fatal("non-allowlisted command", err)
	}
	if _, err = f.svc.Exec(context.Background(), "ghost", "uptime"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown alias", err)
	}
	if _, err = f.svc.Exec(context.Background(), "box", "uptime\nrm -rf /"); !errors.Is(err, ErrInvalid) {
		t.Fatal("multi-line command", err)
	}
	events := f.auditEvents(t)
	if !hasDenied(events, "not allowlisted") || !hasDenied(events, "unknown alias") {
		t.Fatalf("denials missing from audit: %+v", events)
	}
	for _, e := range events {
		if strings.Contains(e.Receipt, "out:uptime") {
			t.Fatal("command output leaked into audit")
		}
	}
}

func TestSudoNeverHost(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return cmd, "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"*\"]\n", "")
	if _, err := f.svc.Exec(context.Background(), "box", "sudo anything"); !errors.Is(err, ErrDenied) {
		t.Fatal("sudo accepted on a sudo-never host")
	}
}

func TestExecWriteGrant(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return "done", "", 0 }}
	extra := "    commands: [\"uptime\"]\n    write_commands: [\"deploy *\"]\n"
	f := openFixture(t, opts, Grants{}, extra, "")
	if _, err := f.svc.Exec(context.Background(), "box", "deploy v1"); !errors.Is(err, ErrDenied) {
		t.Fatal("write command without grant", err)
	}
	f2 := openFixture(t, opts, Grants{Write: true}, extra, "")
	res, err := f2.svc.Exec(context.Background(), "box", "deploy v1")
	if err != nil || !res.Write {
		t.Fatalf("write exec: %v %+v", err, res)
	}
}

func TestExecBoundsAndCancellation(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) {
		return strings.Repeat("x", 5000), "", 0
	}}
	f := openFixture(t, opts, Grants{}, "    commands: [\"*\"]\n    max_output_bytes: 64\n", "")
	res, err := f.svc.Exec(context.Background(), "box", "flood")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) != 64 {
		t.Fatalf("output not bounded: %+v", res)
	}

	slow := sshdOpts{exec: func(cmd string) (string, string, int) {
		time.Sleep(3 * time.Second)
		return "late", "", 0
	}}
	f2 := openFixture(t, slow, Grants{}, "    commands: [\"*\"]\n", "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = f2.svc.Exec(ctx, "box", "hang"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancelled exec", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancelled exec returned after %s", elapsed)
	}
}

func TestHostKeyPinning(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return "", "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n", "")
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); err != nil {
		t.Fatal("pinned key rejected", err)
	}
	// Re-pin to a different key: the dial must fail closed.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	body, _ := os.ReadFile(filepath.Join(f.cfgDir, "config.yaml"))
	pinned := strings.Replace(string(body), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(f.svc.cfg.Hosts["box"].pinned[0]))), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey()))), 1)
	if err := os.WriteFile(filepath.Join(f.cfgDir, "config.yaml"), []byte(pinned), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(Options{ConfigPath: filepath.Join(f.cfgDir, "config.yaml"),
		Auth: identity.Envelope{Schema: identity.Schema, PrincipalID: "me"}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err = svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unpinned host key accepted", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return "", "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n", "")
	f.svc.sem <- struct{}{}
	f.svc.sem <- struct{}{}
	f.svc.sem <- struct{}{}
	f.svc.sem <- struct{}{}
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrBusy) {
		t.Fatal("saturated semaphore admitted work", err)
	}
	<-f.svc.sem
	<-f.svc.sem
	<-f.svc.sem
	<-f.svc.sem
}

func TestClosedService(t *testing.T) {
	opts := sshdOpts{exec: func(cmd string) (string, string, int) { return "", "", 0 }}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n", "")
	f.svc.Close()
	f.svc.Close() // idempotent
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrClosed) {
		t.Fatal("closed service admitted work", err)
	}
}

func TestFilesEndToEnd(t *testing.T) {
	fs := newMemFS()
	fs.files["/etc/app/a.txt"] = []byte("config body")
	extra := "    paths: [\"/etc/app/*\"]\n    write_paths: [\"/srv/app/*\"]\n"
	opts := sshdOpts{fs: fs}
	f := openFixture(t, opts, Grants{Write: true}, extra, "")

	got, err := f.svc.Read(context.Background(), "box", "/etc/app/a.txt")
	if err != nil || got.Text != "config body" || got.Bytes != 11 {
		t.Fatalf("read: %v %+v", err, got)
	}
	if _, err = f.svc.Read(context.Background(), "box", "/etc/shadow"); !errors.Is(err, ErrDenied) {
		t.Fatal("non-allowlisted path read", err)
	}
	if _, err = f.svc.Read(context.Background(), "box", "etc/app/a.txt"); !errors.Is(err, ErrInvalid) {
		t.Fatal("relative path read", err)
	}
	if _, err = f.svc.Read(context.Background(), "box", "/etc/app/missing.txt"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing remote file", err)
	}
	w, err := f.svc.Write(context.Background(), "box", "/srv/app/new.txt", "fresh")
	if err != nil || w.Bytes != 5 {
		t.Fatalf("write: %v %+v", err, w)
	}
	if string(fs.files["/srv/app/new.txt"]) != "fresh" {
		t.Fatal("write did not land in remote fs")
	}
	if _, err = f.svc.Write(context.Background(), "box", "/etc/app/a.txt", "nope"); !errors.Is(err, ErrDenied) {
		t.Fatal("write outside write_paths", err)
	}

	readOnly := openFixture(t, opts, Grants{}, extra, "")
	if _, err = readOnly.svc.Write(context.Background(), "box", "/srv/app/x.txt", "x"); !errors.Is(err, ErrDenied) {
		t.Fatal("write without ssh_write grant", err)
	}
}

func TestHostsViewHidesCredentials(t *testing.T) {
	opts := sshdOpts{}
	f := openFixture(t, opts, Grants{Write: true, Shell: true, Tunnel: true},
		"    commands: [\"uptime\"]\n    write_commands: [\"deploy *\"]\n    paths: [\"/etc/app/*\"]\n    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 5432}\n", "")
	views := f.svc.Hosts()
	if len(views) != 1 || views[0].Alias != "box" || !views[0].Read || !views[0].Write || !views[0].Shell || views[0].Tunnels[0] != "db" {
		t.Fatalf("host view: %+v", views)
	}
	encoded := fmt.Sprintf("%+v", views)
	for _, needle := range []string{"key_ref", "id_ed25519", "host_keys"} {
		if strings.Contains(encoded, needle) {
			t.Fatal("credential material leaked into host view:", needle)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	if _, err := Open(Options{ConfigPath: "relative.yaml"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("relative config path", err)
	}
}
