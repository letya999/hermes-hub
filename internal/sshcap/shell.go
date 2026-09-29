package sshcap

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// shell is one bounded interactive session: a PTY over SSH whose output lands
// in a fixed ring buffer. Idle and absolute lifetimes are enforced by the
// service reaper; the process exit marks the shell done.
type shell struct {
	id        string
	alias     string
	client    *ssh.Client
	session   *ssh.Session
	stdin     io.WriteCloser
	createdAt time.Time
	lifetime  time.Duration

	mu       sync.Mutex
	ring     []byte // fixed buffer, capacity shellBuffer
	base     uint64 // sequence number of ring[0]
	total    uint64 // total bytes ever written
	lastUsed time.Time
	closed   bool
	eof      bool
}

// Write is the output sink for the pump goroutine: appends to the fixed ring,
// dropping the oldest bytes when full.
func (sh *shell) Write(p []byte) (int, error) {
	n := len(p)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if n >= shellBuffer {
		sh.base = sh.total + uint64(n) - shellBuffer
		sh.ring = append(sh.ring[:0], p[n-shellBuffer:]...)
	} else {
		if overflow := len(sh.ring) + n - shellBuffer; overflow > 0 {
			sh.ring = sh.ring[overflow:]
			sh.base += uint64(overflow)
		}
		sh.ring = append(sh.ring, p...)
	}
	sh.total += uint64(n)
	return n, nil
}

func (sh *shell) close() {
	sh.mu.Lock()
	if !sh.closed {
		sh.closed = true
		if sh.session != nil {
			_ = sh.session.Close()
		}
		if sh.client != nil {
			_ = sh.client.Close()
		}
	}
	sh.mu.Unlock()
}

// expired reports whether the shell passed its idle or absolute lifetime.
func (sh *shell) expired(now time.Time, idle, lifetime time.Duration) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.closed || now.Sub(sh.lastUsed) > idle || now.Sub(sh.createdAt) > lifetime
}

type ShellView struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Next      uint64 `json:"next_offset"`
	EOF       bool   `json:"eof"`
	ExpiresAt int64  `json:"expires_at_unix"`
}

type ShellReadResult struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	Next uint64 `json:"next_offset"`
	EOF  bool   `json:"eof"`
	Lost uint64 `json:"lost_bytes,omitempty"`
}

// ShellOpen starts a PTY shell on an allowlisted host. The ssh_shell grant is
// required; the shell counts against the shell cap, not the exec semaphore.
func (s *Service) ShellOpen(ctx context.Context, alias string) (*ShellView, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	if !s.grants.Shell {
		return nil, fmt.Errorf("%w: interactive shell requires the ssh_shell grant", ErrDenied)
	}
	h, err := s.host(alias)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.shells)+s.pendingShells >= s.maxShells() {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: shell limit reached", ErrBusy)
	}
	s.pendingShells++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pendingShells--
		s.mu.Unlock()
	}()
	client, err := s.dial(ctx, alias, h)
	if err != nil {
		s.auditEvent("ssh-shell", alias, "error", "dial failed")
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: session: %v", ErrUnavailable, err)
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err = session.RequestPty("xterm", 24, 80, modes); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w: pty: %v", ErrUnavailable, err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w: stdin: %v", ErrUnavailable, err)
	}
	out, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w: stdout: %v", ErrUnavailable, err)
	}
	if err = session.Shell(); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("%w: shell: %v", ErrUnavailable, err)
	}
	sh := &shell{id: newID("sh"), alias: alias, client: client, session: session, stdin: stdin, createdAt: time.Now(), lifetime: s.shellLifetime(), lastUsed: time.Now()}
	go func() {
		_, _ = io.Copy(sh, out)
		sh.mu.Lock()
		sh.eof = true
		sh.mu.Unlock()
	}()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sh.close()
		return nil, ErrClosed
	}
	s.shells[sh.id] = sh
	s.mu.Unlock()
	s.auditEvent("ssh-shell", alias, "ok", "shell "+sh.id+" opened")
	return sh.view(), nil
}

func (sh *shell) view() *ShellView {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return &ShellView{ID: sh.id, Alias: sh.alias, Next: sh.total, EOF: sh.eof, ExpiresAt: sh.createdAt.Add(sh.lifetime).Unix()}
}

// ShellSend writes input to the shell. Input is bounded to 4 KiB per call.
func (s *Service) ShellSend(id, data string) error {
	if err := s.alive(); err != nil {
		return err
	}
	sh, err := s.shellByID(id)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return fmt.Errorf("%w: shell input is limited to 4 KiB", ErrInvalid)
	}
	sh.mu.Lock()
	sh.lastUsed = time.Now()
	closed := sh.closed || sh.eof
	sh.mu.Unlock()
	if closed {
		return fmt.Errorf("%w: shell is closed", ErrClosed)
	}
	if _, err = sh.stdin.Write([]byte(data)); err != nil {
		return fmt.Errorf("%w: shell write: %v", ErrUnavailable, err)
	}
	return nil
}

// ShellRead returns new output from the ring at or after offset. Bytes dropped
// by the fixed ring are reported as lost_bytes; the offset clamp never
// rewinds below the ring base.
func (s *Service) ShellRead(id string, offset uint64) (*ShellReadResult, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	sh, err := s.shellByID(id)
	if err != nil {
		return nil, err
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.lastUsed = time.Now()
	start := offset
	lost := uint64(0)
	if start < sh.base {
		lost = sh.base - start
		start = sh.base
	}
	idx := int(start - sh.base)
	data := ""
	if idx < len(sh.ring) {
		data = string(sh.ring[idx:])
	}
	return &ShellReadResult{ID: sh.id, Data: data, Next: sh.total, EOF: sh.eof, Lost: lost}, nil
}

// ShellClose ends one shell deterministically.
func (s *Service) ShellClose(id string) error {
	sh, err := s.shellByID(id)
	if err != nil {
		return err
	}
	sh.close()
	s.mu.Lock()
	delete(s.shells, id)
	s.mu.Unlock()
	s.auditEvent("ssh-shell", sh.alias, "ok", "shell "+id+" closed")
	return nil
}

func (s *Service) shellByID(id string) (*shell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shells[id]
	if !ok {
		return nil, fmt.Errorf("%w: unknown shell %q", ErrNotFound, id)
	}
	return sh, nil
}

func (s *Service) maxShells() int {
	if s.cfg.Defaults.MaxShells <= 0 {
		return 2
	}
	return s.cfg.Defaults.MaxShells
}

func (s *Service) shellIdle() time.Duration {
	if s.cfg.Defaults.ShellIdle <= 0 {
		return defaultShellIdle * time.Second
	}
	return time.Duration(s.cfg.Defaults.ShellIdle) * time.Second
}

func (s *Service) shellLifetime() time.Duration {
	if s.cfg.Defaults.ShellLifetime <= 0 {
		return defaultShellTTL * time.Second
	}
	return time.Duration(s.cfg.Defaults.ShellLifetime) * time.Second
}
