package exposure

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"golang.org/x/crypto/ssh"
)

// sishd is a minimal in-process sish relay for exercising the provider end
// to end: pubkey auth, tcpip-forward global requests and test-driven
// forwarded-tcpip channel delivery.
type sishd struct {
	addr    string
	hostKey ssh.PublicKey
	ln      net.Listener

	mu    sync.Mutex
	binds map[*ssh.ServerConn][]string
	wg    sync.WaitGroup
}

func startSishd(t *testing.T, auth ssh.PublicKey) *sishd {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if auth == nil || string(key.Marshal()) == string(auth.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &sishd{addr: ln.Addr().String(), hostKey: signer.PublicKey(), ln: ln, binds: map[*ssh.ServerConn][]string{}}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				d.serve(c, cfg)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		d.mu.Lock()
		for conn := range d.binds {
			_ = conn.Close()
		}
		d.mu.Unlock()
		d.wg.Wait()
	})
	return d
}

func (d *sishd) serve(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	d.mu.Lock()
	d.binds[conn] = nil
	d.mu.Unlock()
	go d.requests(conn, reqs)
	for ch := range chans {
		_ = ch.Reject(ssh.UnknownChannelType, "no client channels")
	}
}

func (d *sishd) requests(conn *ssh.ServerConn, reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type != "tcpip-forward" {
			_ = req.Reply(false, nil)
			continue
		}
		var fwd struct {
			BindAddr string
			BindPort uint32
		}
		if err := ssh.Unmarshal(req.Payload, &fwd); err != nil {
			_ = req.Reply(false, nil)
			continue
		}
		d.mu.Lock()
		d.binds[conn] = append(d.binds[conn], net.JoinHostPort(fwd.BindAddr, fmt.Sprint(fwd.BindPort)))
		d.mu.Unlock()
		_ = req.Reply(true, nil)
	}
}

// deliver opens one forwarded-tcpip channel to the bound client, writes
// payload and reads exactly len(payload) bytes back — the test target echoes.
func (d *sishd) deliver(t *testing.T, bind, payload string) string {
	t.Helper()
	d.mu.Lock()
	var conn *ssh.ServerConn
	for c, binds := range d.binds {
		for _, b := range binds {
			if b == bind {
				conn = c
			}
		}
	}
	d.mu.Unlock()
	if conn == nil {
		t.Fatalf("no tcpip-forward registered for %q", bind)
	}
	host, portText, err := net.SplitHostPort(bind)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ch, reqs, err := conn.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
		ConnectedAddr string
		ConnectedPort uint32
		OriginAddr    string
		OriginPort    uint32
	}{host, uint32(port), "203.0.113.9", 51000}))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	if _, err = ch.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(payload))
	if _, err = io.ReadFull(ch, out); err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// startEcho runs a loopback echo server and returns its host:port.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// sishFixture writes a real config + file: key and loads it through Load so
// pinned host keys populate exactly like production. Returns the PEM so
// tests can also feed it to a fake broker.
func sishFixture(t *testing.T, target string) (*Config, *sishd, *os.Root, []byte) {
	t.Helper()
	privatePEM, publicLine, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	auth, _, _, _, err := ssh.ParseAuthorizedKey(publicLine)
	if err != nil {
		t.Fatal(err)
	}
	d := startSishd(t, auth)
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "key.pem"), privatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	relayHost, relayPort, err := net.SplitHostPort(d.addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(relayPort)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(d.hostKey)))
	body := fmt.Sprintf(`schema: 1
provider: sish
relay:
  host: %s
  port: %d
  user: u
  domain: hub.example.com
  host_keys:
    - "%s"
zone: z
key_ref: "file:key.pem"
endpoints:
  demo:
    target: %s
`, relayHost, port, hostKey, target)
	if err = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return cfg, d, root, privatePEM
}

// fakeBroker serves acquire/materialize/release like Credential Broker.
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

func TestSishProviderBrokerKeyRef(t *testing.T) {
	target := startEcho(t)
	cfg, d, root, privatePEM := sishFixture(t, target)
	cfg.KeyRef = "broker:grant-01"
	var err error
	released := &atomic.Int32{}
	srv := fakeBroker(t, map[string]string{"SSH_PRIVATE_KEY": string(privatePEM)}, released)
	p := NewSishProvider(cfg, ProviderOptions{Root: root, Broker: brokerConfig(t, srv),
		Auth: identity.Envelope{Schema: identity.Schema, PrincipalID: "me", ContextID: "me", RuntimeID: "rt", PolicyVersion: "p"}})
	handle, err := p.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: target, PublicName: "demo.z.hub.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(context.Background(), handle)
	if got := d.deliver(t, "demo.z:80", "ping"); got != "ping" {
		t.Fatalf("forward returned %q", got)
	}
	if released.Load() < 1 {
		t.Fatal("broker lease was not released")
	}
}

func TestSishProviderRejectsUnpinnedHostKey(t *testing.T) {
	target := startEcho(t)
	cfg, _, root, _ := sishFixture(t, target)
	_, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Relay.HostKeys = []string{strings.TrimSpace(string(pub))}
	if err = cfg.Relay.validate(); err != nil {
		t.Fatal(err)
	}
	p := NewSishProvider(cfg, ProviderOptions{Root: root})
	if _, err = p.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: target, PublicName: "demo.z.hub.example.com"}); err == nil {
		t.Fatal("unpinned relay host key must fail closed")
	}
}

func TestSishProviderServiceTarget(t *testing.T) {
	target := startEcho(t)
	cfg, d, root, _ := sishFixture(t, "service:x:1")
	p := NewSishProvider(cfg, ProviderOptions{Root: root})
	_, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: "service:localhost:" + port, PublicName: "demo.z.hub.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(context.Background(), handle)
	if got := d.deliver(t, "demo.z:80", "ping"); got != "ping" {
		t.Fatalf("service target forward returned %q", got)
	}
}

func TestSishProviderHubListenerTarget(t *testing.T) {
	cfg, d, root, _ := sishFixture(t, TargetHubListener)
	p := NewSishProvider(cfg, ProviderOptions{Root: root})
	if _, err := p.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: TargetHubListener, PublicName: "demo.z.hub.example.com"}); err == nil {
		t.Fatal("hub listener without a rendered public address must fail closed")
	}
	p2 := NewSishProvider(cfg, ProviderOptions{Root: root, Public: startEcho(t)})
	handle, err := p2.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: TargetHubListener, PublicName: "demo.z.hub.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close(context.Background(), handle)
	if got := d.deliver(t, "demo.z:80", "ping"); got != "ping" {
		t.Fatalf("hub listener forward returned %q", got)
	}
}

func TestSishProviderOpenForwardClose(t *testing.T) {
	target := startEcho(t)
	cfg, d, root, _ := sishFixture(t, target)
	p := NewSishProvider(cfg, ProviderOptions{Root: root})
	handle, err := p.Open(context.Background(), Spec{Provider: "sish", Endpoint: "demo",
		Bind: "demo.z", Target: target, PublicName: "demo.z.hub.example.com", Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.deliver(t, "demo.z:80", "ping"); got != "ping" {
		t.Fatalf("forward returned %q", got)
	}
	if err = p.Close(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(context.Background(), handle); err == nil {
		t.Fatal("closing a closed handle must fail")
	}
}
