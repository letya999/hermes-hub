package sshcap

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// tunnel is one managed forward: a loopback listener inside the runtime whose
// connections are proxied over SSH to a pre-approved remote endpoint. Nothing
// is published beyond the container's loopback.
type tunnel struct {
	id       string
	alias    string
	name     string
	remote   string
	listener net.Listener
	client   *ssh.Client
	created  time.Time
	lifetime time.Duration
	idle     time.Duration

	mu       sync.Mutex
	active   int
	lastUsed time.Time
	closed   bool
}

func (t *tunnel) close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		_ = t.listener.Close()
		if t.client != nil {
			_ = t.client.Close()
		}
	}
	t.mu.Unlock()
}

func (t *tunnel) expired(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed || (t.active == 0 && now.Sub(t.lastUsed) > t.idle) || now.Sub(t.created) > t.lifetime
}

type TunnelView struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Name      string `json:"name"`
	LocalAddr string `json:"local_addr"`
	Remote    string `json:"remote"`
	ExpiresAt int64  `json:"expires_at_unix"`
}

// TunnelOpen creates a managed forward for a configured tunnel entry. The
// ssh_tunnel grant is required; remote endpoints come only from config.
func (s *Service) TunnelOpen(ctx context.Context, alias, name string) (*TunnelView, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	if !s.grants.Tunnel {
		return nil, fmt.Errorf("%w: tunnels require the ssh_tunnel grant", ErrDenied)
	}
	h, err := s.host(alias)
	if err != nil {
		return nil, err
	}
	fwd, ok := h.Tunnels[name]
	if !ok {
		return nil, fmt.Errorf("%w: tunnel %q is not configured on %q", ErrNotFound, name, alias)
	}
	s.mu.Lock()
	if len(s.tunnels)+s.pendingTunnels >= s.maxTunnels() {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: tunnel limit reached", ErrBusy)
	}
	for _, t := range s.tunnels {
		if t.alias == alias && t.name == name {
			view := t.view()
			s.mu.Unlock()
			return view, nil // idempotent: the existing managed tunnel is returned
		}
	}
	s.pendingTunnels++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pendingTunnels--
		s.mu.Unlock()
	}()
	client, err := s.dial(ctx, alias, h)
	if err != nil {
		s.auditEvent("ssh-tunnel", alias, "error", "dial failed")
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: listen: %v", ErrUnavailable, err)
	}
	t := &tunnel{id: newID("tun"), alias: alias, name: name, remote: fmt.Sprintf("%s:%d", fwd.RemoteHost, fwd.RemotePort), listener: listener, client: client, created: time.Now(), lifetime: s.tunnelLifetime(), idle: s.tunnelIdle(), lastUsed: time.Now()}
	go t.accept()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		t.close()
		return nil, ErrClosed
	}
	s.tunnels[t.id] = t
	s.mu.Unlock()
	s.auditEvent("ssh-tunnel", alias, "ok", "tunnel "+name+" "+listener.Addr().String()+"->"+t.remote)
	return t.view(), nil
}

func (t *tunnel) view() *TunnelView {
	return &TunnelView{ID: t.id, Alias: t.alias, Name: t.name, LocalAddr: t.listener.Addr().String(), Remote: t.remote, ExpiresAt: t.created.Add(t.lifetime).Unix()}
}

func (t *tunnel) accept() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			return
		}
		go t.forward(conn)
	}
}

func (t *tunnel) forward(conn net.Conn) {
	defer conn.Close()
	t.mu.Lock()
	t.active++
	t.lastUsed = time.Now()
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.active--
		t.lastUsed = time.Now()
		t.mu.Unlock()
	}()
	remote, err := t.client.Dial("tcp", t.remote)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, remote); done <- struct{}{} }()
	<-done
}

// TunnelList reports open managed tunnels.
func (s *Service) TunnelList() []TunnelView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TunnelView, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		out = append(out, *t.view())
	}
	return out
}

// TunnelClose tears down one managed tunnel deterministically.
func (s *Service) TunnelClose(id string) error {
	t, err := s.tunnelByID(id)
	if err != nil {
		return err
	}
	t.close()
	s.mu.Lock()
	delete(s.tunnels, id)
	s.mu.Unlock()
	s.auditEvent("ssh-tunnel", t.alias, "ok", "tunnel "+t.name+" closed")
	return nil
}

func (s *Service) tunnelByID(id string) (*tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tunnels[id]
	if !ok {
		return nil, fmt.Errorf("%w: unknown tunnel %q", ErrNotFound, id)
	}
	return t, nil
}

func (s *Service) maxTunnels() int {
	if s.cfg.Defaults.MaxTunnels <= 0 {
		return 4
	}
	return s.cfg.Defaults.MaxTunnels
}

func (s *Service) tunnelIdle() time.Duration {
	if s.cfg.Defaults.TunnelIdle <= 0 {
		return defaultTunnelIdle * time.Second
	}
	return time.Duration(s.cfg.Defaults.TunnelIdle) * time.Second
}

func (s *Service) tunnelLifetime() time.Duration {
	if s.cfg.Defaults.TunnelLifetime <= 0 {
		return defaultTunnelTTL * time.Second
	}
	return time.Duration(s.cfg.Defaults.TunnelLifetime) * time.Second
}

// reap is the single periodic sweeper: expired shells and tunnels are closed
// and removed. It stops on Close.
func (s *Service) reap() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-ticker.C:
			s.sweep(now)
		}
	}
}

func (s *Service) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sh := range s.shells {
		if sh.expired(now, s.shellIdle(), s.shellLifetime()) {
			sh.close()
			delete(s.shells, id)
		}
	}
	for id, t := range s.tunnels {
		if t.expired(now) {
			t.close()
			delete(s.tunnels, id)
		}
	}
}
