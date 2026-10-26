package exposure

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ProviderOptions carries the runtime dependencies of the sish provider.
// Root scopes file: credential refs to the reviewed config directory;
// Broker + Auth drive broker: key_ref leases.
type ProviderOptions struct {
	MaxConnections    int
	ConnectionTimeout time.Duration
	Root              *os.Root
	Broker            credentialbroker.Config
	Auth              identity.Envelope
	Public            string
}

// SishProvider implements Provider as an in-process SSH remote forward —
// the workload `ssh -R <bind>:443:<target>` performs, expressed over
// x/crypto/ssh so no extra binary or container admission is required.
type SishProvider struct {
	slots chan struct{}
	cfg   *Config
	opts  ProviderOptions

	mu   sync.Mutex
	live map[string]*sishTunnel
	next int
}

type sishTunnel struct {
	slots   chan struct{}
	timeout time.Duration
	client  *ssh.Client
	ln      net.Listener
	target  string
	done    chan struct{}
}

// NewSishProvider binds the provider to the reviewed config; the file:
// credential resolves per Open from Root.
func NewSishProvider(cfg *Config, opts ProviderOptions) *SishProvider {
	p := &SishProvider{cfg: cfg, opts: opts, live: map[string]*sishTunnel{}}
	if opts.MaxConnections > 0 {
		p.slots = make(chan struct{}, min(opts.MaxConnections, 1024))
	}
	return p
}

// Open dials the relay, requests the remote forward for spec.Bind and
// starts splicing inbound channels to the target.
func (p *SishProvider) Open(ctx context.Context, spec Spec) (string, error) {
	target, err := p.resolveTarget(spec.Target)
	if err != nil {
		return "", err
	}
	signer, cleanup, err := p.signer(ctx)
	if err != nil {
		return "", err
	}
	defer cleanup()
	dialer := net.Dialer{Timeout: 15 * time.Second}
	addr := p.cfg.Relay.Address()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("%w: relay dial: %v", ErrUnavailable, err)
	}
	clientConfig := &ssh.ClientConfig{
		User:            p.cfg.Relay.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: p.cfg.Relay.hostKeyCallback(),
		Timeout:         15 * time.Second,
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return "", ErrUnavailable
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("%w: relay handshake: %v", ErrUnavailable, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	ln, err := client.Listen("tcp", net.JoinHostPort(spec.Bind, "443"))
	if err != nil {
		_ = client.Close()
		return "", fmt.Errorf("%w: remote forward %q refused", ErrDenied, spec.Bind)
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return "", ErrUnavailable
	}
	tn := &sishTunnel{slots: p.slots, timeout: p.opts.ConnectionTimeout, client: client, ln: ln, target: target, done: make(chan struct{})}
	p.mu.Lock()
	p.next++
	handle := fmt.Sprintf("sish-%d", p.next)
	p.live[handle] = tn
	p.mu.Unlock()
	go tn.serve()
	if p.opts.ConnectionTimeout > 0 {
		go tn.keepalive()
	}
	return handle, nil
}

// Close tears the remote forward down; an unknown handle fails closed.
func (p *SishProvider) Close(_ context.Context, handle string) error {
	p.mu.Lock()
	tn, ok := p.live[handle]
	if ok {
		delete(p.live, handle)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: tunnel handle %q", ErrNotFound, handle)
	}
	_ = tn.ln.Close()
	_ = tn.client.Close()
	<-tn.done
	return nil
}

// Shutdown releases the config root the provider scopes file: keys to.
func (p *SishProvider) Shutdown() {
	if p.opts.Root != nil {
		_ = p.opts.Root.Close()
	}
}

// serve accepts forwarded-tcpip connections and splices each to the target.
func (tn *sishTunnel) serve() {
	defer close(tn.done)
	for {
		inbound, err := tn.ln.Accept()
		if err != nil {
			return
		}
		if tn.slots != nil {
			select {
			case tn.slots <- struct{}{}:
			default:
				_ = inbound.Close()
				continue
			}
			go func(conn net.Conn) { defer func() { <-tn.slots }(); tn.splice(conn) }(inbound)
		} else {
			go tn.splice(inbound)
		}
	}
}

func (tn *sishTunnel) splice(inbound net.Conn) {
	if tn.timeout > 0 {
		_ = inbound.SetDeadline(time.Now().Add(tn.timeout))
	}
	target, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", tn.target)
	if err != nil {
		_ = inbound.Close()
		return
	}
	if tn.timeout > 0 {
		_ = target.SetDeadline(time.Now().Add(tn.timeout))
	}
	go func() {
		_, _ = io.Copy(target, inbound)
		_ = target.Close()
	}()
	_, _ = io.Copy(inbound, target)
	_ = inbound.Close()
}

// resolveTarget maps the declared target to a dial address. service: and
// literal targets carry their own address; the reserved listener resolves
// from the rendered public-listener address and fails closed without one.
func (p *SishProvider) resolveTarget(target string) (string, error) {
	if target == TargetHubListener {
		if strings.TrimSpace(p.opts.Public) == "" {
			return "", fmt.Errorf("%w: hub public listener is not configured", ErrUnavailable)
		}
		return p.opts.Public, nil
	}
	return strings.TrimPrefix(target, "service:"), nil
}

// signer resolves key_ref to one SSH signer: file: reads below the reviewed
// config dir; broker: runs acquire -> materialize -> release. The private
// half never leaves memory for anything but the handshake.
func (p *SishProvider) signer(ctx context.Context) (ssh.Signer, func(), error) {
	scheme, rest, _ := strings.Cut(p.cfg.KeyRef, ":")
	cleanup := func() {}
	var raw []byte
	switch scheme {
	case "file":
		f, err := p.opts.Root.Open(rest)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: credential file %q: %v", ErrUnavailable, rest, err)
		}
		body, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
		_ = f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: credential file %q unreadable", ErrUnavailable, rest)
		}
		raw = body
	case "broker":
		body, release, err := p.materializeKey(ctx, rest)
		if err != nil {
			return nil, nil, err
		}
		raw = body
		cleanup = release
	default:
		return nil, nil, fmt.Errorf("%w: unsupported key_ref scheme", ErrInvalid)
	}
	defer clear(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("%w: relay private key does not parse", ErrUnavailable)
	}
	return signer, cleanup, nil
}

// materializeKey runs the Broker lease for the exposure binding.
func (p *SishProvider) materializeKey(ctx context.Context, grantID string) ([]byte, func(), error) {
	control, err := p.opts.Broker.NewForBinding(p.opts.Auth, "broker:control", "exposure", "hermes-runtime")
	if err != nil {
		return nil, nil, err
	}
	runtime, err := p.opts.Broker.NewForBinding(p.opts.Auth, "broker:runtime", "exposure", "hermes-runtime")
	if err != nil {
		return nil, nil, err
	}
	lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: grantID, TTLSeconds: 60})
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		_ = runtime.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{})
	}
	materialized, err := runtime.Materialize(ctx, lease.ID)
	if err != nil {
		release()
		return nil, nil, err
	}
	value := materialized.Env["SSH_PRIVATE_KEY"]
	clear(materialized.Env)
	return []byte(value), release, nil
}

// hostKeyCallback pins the relay server key bytes — anything else is
// denied, never prompted.
func (r *Relay) hostKeyCallback() ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		for _, pinned := range r.pinned {
			if bytes.Equal(pinned.Marshal(), key.Marshal()) {
				return nil
			}
		}
		return fmt.Errorf("%w: relay host key is not pinned", ErrDenied)
	}
}

// Alive reports whether a forward's accept loop still exists; hosting reconciles lost SSH connections.
func (p *SishProvider) Alive(handle string) bool {
	p.mu.Lock()
	tn, ok := p.live[handle]
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-tn.done:
		return false
	default:
		return true
	}
}

// Bound detection of a silent relay loss so reconciliation can restore the route.
func (tn *sishTunnel) keepalive() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-tn.done:
			return
		case <-ticker.C:
			done := make(chan error, 1)
			go func() { _, _, err := tn.client.SendRequest("keepalive@openssh.com", true, nil); done <- err }()
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-tn.done:
				timer.Stop()
				return
			case err := <-done:
				timer.Stop()
				if err != nil {
					_ = tn.client.Close()
					return
				}
			case <-timer.C:
				_ = tn.client.Close()
				return
			}
		}
	}
}
