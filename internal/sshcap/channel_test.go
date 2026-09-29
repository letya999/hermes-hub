package sshcap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"golang.org/x/crypto/ssh"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestShellEndToEnd(t *testing.T) {
	opts := sshdOpts{shell: true}
	f := openFixture(t, opts, Grants{Shell: true}, "    commands: [\"uptime\"]\n", "")
	view, err := f.svc.ShellOpen(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(view.ID, "sh-") || view.Alias != "box" {
		t.Fatalf("view: %+v", view)
	}
	if err = f.svc.ShellSend(view.ID, "ping\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var out *ShellReadResult
	for time.Now().Before(deadline) {
		out, err = f.svc.ShellRead(view.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.Data, "ping") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out == nil || !strings.Contains(out.Data, "ping") {
		t.Fatalf("echo never arrived: %+v", out)
	}
	next, err := f.svc.ShellRead(view.ID, out.Next)
	if err != nil {
		t.Fatal(err)
	}
	if next.Data != "" || next.Lost != 0 {
		t.Fatalf("offset read returned phantom data: %+v", next)
	}
	if err = f.svc.ShellClose(view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ShellRead(view.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed shell still readable", err)
	}
	if err = f.svc.ShellSend(view.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("send to closed shell", err)
	}
}

func TestShellGrantAndLimit(t *testing.T) {
	opts := sshdOpts{shell: true}
	f := openFixture(t, opts, Grants{}, "    commands: [\"uptime\"]\n", "")
	if _, err := f.svc.ShellOpen(context.Background(), "box"); !errors.Is(err, ErrDenied) {
		t.Fatal("shell without grant", err)
	}
	one := openFixture(t, opts, Grants{Shell: true}, "    commands: [\"uptime\"]\n", "defaults: {max_shells: 1}\n")
	view, err := one.svc.ShellOpen(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	defer one.svc.ShellClose(view.ID)
	if _, err = one.svc.ShellOpen(context.Background(), "box"); !errors.Is(err, ErrBusy) {
		t.Fatal("shell limit not enforced", err)
	}
}

func TestShellRingBounds(t *testing.T) {
	sh := &shell{id: "sh-x"}
	big := strings.Repeat("y", shellBuffer+100)
	if _, err := sh.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if sh.total != uint64(len(big)) || len(sh.ring) != shellBuffer || sh.base != 100 {
		t.Fatalf("ring state: total=%d len=%d base=%d", sh.total, len(sh.ring), sh.base)
	}
	sh2 := &shell{id: "sh-y", ring: []byte(strings.Repeat("z", shellBuffer)), base: 0, total: uint64(shellBuffer)}
	if _, err := sh2.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if sh2.base != 3 || len(sh2.ring) != shellBuffer || !strings.HasSuffix(string(sh2.ring), "new") {
		t.Fatalf("overflow eviction wrong: base=%d len=%d", sh2.base, len(sh2.ring))
	}
	svc := &Service{shells: map[string]*shell{"sh-y": sh2}, done: make(chan struct{})}
	res, err := svc.ShellRead("sh-y", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lost != 3 || res.Next != uint64(shellBuffer)+3 {
		t.Fatalf("lost bytes not reported: %+v", res)
	}
}

func TestShellExpiry(t *testing.T) {
	now := time.Now()
	sh := &shell{createdAt: now.Add(-time.Hour), lastUsed: now.Add(-time.Hour)}
	if !sh.expired(now, 10*time.Minute, 2*time.Hour) {
		t.Fatal("idle shell not expired")
	}
	sh.lastUsed = now
	if !sh.expired(now, 10*time.Minute, 30*time.Minute) {
		t.Fatal("lifetime cap not enforced on an active shell")
	}
	sh.createdAt = now
	if sh.expired(now, 10*time.Minute, 30*time.Minute) {
		t.Fatal("fresh shell expired")
	}
}

// echoListener is a plain TCP echo server: the tunnel remote target.
func echoListener(t *testing.T) (addr string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		}
	}()
	parts := strings.Split(ln.Addr().String(), ":")
	var p int
	_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &p)
	return ln.Addr().String(), p
}

func TestTunnelEndToEnd(t *testing.T) {
	_, port := echoListener(t)
	opts := sshdOpts{directTCPIP: true}
	extra := fmt.Sprintf("    commands: [\"uptime\"]\n    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: %d}\n      cache: {remote_host: 127.0.0.1, remote_port: %d}\n", port, port)
	f := openFixture(t, opts, Grants{Tunnel: true}, extra, "")
	view, err := f.svc.TunnelOpen(context.Background(), "box", "db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(view.LocalAddr, "127.0.0.1:") {
		t.Fatal("tunnel listener is not loopback:", view.LocalAddr)
	}
	conn, err := net.DialTimeout("tcp", view.LocalAddr, 3*time.Second)
	if err != nil {
		t.Fatal("tunnel dial", err)
	}
	if _, err = conn.Write([]byte("through-ssh")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "through-ssh" {
		t.Fatalf("tunnel data: %v %q", err, buf[:n])
	}
	_ = conn.Close()
	again, err := f.svc.TunnelOpen(context.Background(), "box", "db")
	if err != nil || again.ID != view.ID {
		t.Fatal("idempotent reopen returned a different tunnel")
	}
	if list := f.svc.TunnelList(); len(list) != 1 || list[0].Name != "db" {
		t.Fatalf("list: %+v", list)
	}
	if err = f.svc.TunnelClose(view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = net.DialTimeout("tcp", view.LocalAddr, 500*time.Millisecond); err == nil {
		t.Fatal("listener survived tunnel close")
	}
	if err = f.svc.TunnelClose(view.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("double close", err)
	}
}

func TestTunnelGrantConfigAndLimit(t *testing.T) {
	_, port := echoListener(t)
	opts := sshdOpts{directTCPIP: true}
	extra := fmt.Sprintf("    commands: [\"uptime\"]\n    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: %d}\n      other: {remote_host: 127.0.0.1, remote_port: %d}\n", port, port)
	f := openFixture(t, opts, Grants{}, extra, "")
	if _, err := f.svc.TunnelOpen(context.Background(), "box", "db"); !errors.Is(err, ErrDenied) {
		t.Fatal("tunnel without grant", err)
	}
	g := openFixture(t, opts, Grants{Tunnel: true}, extra, "defaults: {max_tunnels: 1}\n")
	if _, err := g.svc.TunnelOpen(context.Background(), "box", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unconfigured tunnel", err)
	}
	view, err := g.svc.TunnelOpen(context.Background(), "box", "db")
	if err != nil {
		t.Fatal(err)
	}
	defer g.svc.TunnelClose(view.ID)
	if _, err = g.svc.TunnelOpen(context.Background(), "box", "other"); !errors.Is(err, ErrBusy) {
		t.Fatal("tunnel limit not enforced", err)
	}
}

func TestTunnelExpiry(t *testing.T) {
	now := time.Now()
	tn := &tunnel{created: now.Add(-2 * time.Hour), lastUsed: now, lifetime: time.Hour, idle: time.Minute}
	if !tn.expired(now) {
		t.Fatal("lifetime cap not enforced")
	}
	tn2 := &tunnel{created: now, lastUsed: now.Add(-10 * time.Minute), lifetime: time.Hour, idle: time.Minute}
	if !tn2.expired(now) {
		t.Fatal("idle tunnel not expired")
	}
	tn2.active = 1
	if tn2.expired(now) {
		t.Fatal("active tunnel treated as idle")
	}
}

// fakeBroker serves acquire/materialize/release over TLS like Credential Broker.
func fakeBroker(t *testing.T, env map[string]string, released *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/leases", func(w http.ResponseWriter, r *http.Request) {
		var in brokerv1.AcquireLease
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.GrantID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease-1", GrantID: in.GrantID, CredentialID: "cred-1", Revision: 1, ExpiresAt: time.Now().Add(time.Minute)})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/materialize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease-1", ExpiresAt: time.Now().Add(time.Minute), Env: env})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/release", func(w http.ResponseWriter, r *http.Request) {
		released.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func brokerConfig(t *testing.T, srv *httptest.Server) credentialbroker.Config {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "runtime.key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	return credentialbroker.Config{URL: srv.URL, KeyFile: keyPath, KeyID: "hermes-runtime", Issuer: "hermes-hub", HTTPClient: srv.Client()}
}

func TestBrokerKeyMaterialization(t *testing.T) {
	keyPEM, keyPub := clientKeyPEM(t)
	released := &atomic.Int32{}
	srv := fakeBroker(t, map[string]string{"SSH_PRIVATE_KEY": string(keyPEM)}, released)
	opts := sshdOpts{authKey: keyPub, exec: func(cmd string) (string, string, int) { return "ok", "", 0 }}
	d := startSSHD(t, opts)
	parts := strings.Split(d.addr, ":")
	dir := t.TempDir()
	// Build config manually: broker key_ref, pinned host key from the test sshd.
	body := "schema: 1\nhosts:\n  box:\n    host: 127.0.0.1\n    port: " + parts[len(parts)-1] +
		"\n    user: tester\n    host_keys: [\"" + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(d.hostKey))) + "\"]\n    key_ref: \"broker:grant-ssh-1\"\n    commands: [\"uptime\"]\n"
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	svc, err := Open(Options{ConfigPath: configPath,
		Auth:   identity.Envelope{Schema: identity.Schema, PrincipalID: "me", ContextID: "me", RuntimeID: "rt", PolicyVersion: "p"},
		Broker: brokerConfig(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	res, err := svc.Exec(context.Background(), "box", "uptime")
	if err != nil || res.Stdout != "ok" {
		t.Fatalf("broker-backed exec: %v %+v", err, res)
	}
	if released.Load() < 1 {
		t.Fatal("broker lease was not released")
	}

	// Missing env var: the lease still must be released.
	released.Store(0)
	srv2 := fakeBroker(t, map[string]string{"OTHER": "x"}, released)
	svc2, err := Open(Options{ConfigPath: configPath,
		Auth:   identity.Envelope{Schema: identity.Schema, PrincipalID: "me", ContextID: "me", RuntimeID: "rt", PolicyVersion: "p"},
		Broker: brokerConfig(t, srv2)})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	if _, err = svc2.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing env key materialized", err)
	}
	if released.Load() < 1 {
		t.Fatal("lease not released on materialize failure")
	}

	// No broker configured: fail closed.
	svc3, err := Open(Options{ConfigPath: configPath,
		Auth:   identity.Envelope{Schema: identity.Schema, PrincipalID: "me", ContextID: "me", RuntimeID: "rt", PolicyVersion: "p"},
		Broker: credentialbroker.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc3.Close()
	if _, err = svc3.Exec(context.Background(), "box", "uptime"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("broker ref without broker", err)
	}
}
