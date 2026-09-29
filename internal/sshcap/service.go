package sshcap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Grants gate the privileged effects. Read/diagnostic access is always part
// of the capability; write, interactive shell and tunnels are separate.
type Grants struct {
	Write  bool
	Shell  bool
	Tunnel bool
}

type Options struct {
	ConfigPath string
	Grants     Grants
	Auth       identity.Envelope
	Broker     credentialbroker.Config
	AuditPath  string // absolute path of the per-owner audit ledger
}

type Service struct {
	cfg    *Config
	root   *os.Root
	grants Grants
	auth   identity.Envelope
	broker credentialbroker.Config
	ledger *audit.Ledger
	sem    chan struct{}

	mu             sync.Mutex
	closed         bool
	shells         map[string]*shell
	tunnels        map[string]*tunnel
	pendingShells  int
	pendingTunnels int
	done           chan struct{}
}

func Open(opts Options) (*Service, error) {
	if !filepath.IsAbs(opts.ConfigPath) {
		return nil, fmt.Errorf("%w: config path must be absolute", ErrInvalid)
	}
	cfg, err := Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(opts.ConfigPath))
	if err != nil {
		return nil, fmt.Errorf("%w: config dir: %v", ErrUnavailable, err)
	}
	var ledger *audit.Ledger
	if opts.AuditPath != "" {
		ledger, err = audit.Open(opts.AuditPath)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	sessions := cfg.Defaults.MaxSessions
	if sessions <= 0 {
		sessions = 4
	}
	s := &Service{cfg: cfg, root: root, grants: opts.Grants, auth: opts.Auth, broker: opts.Broker, ledger: ledger, sem: make(chan struct{}, sessions), shells: map[string]*shell{}, tunnels: map[string]*tunnel{}, done: make(chan struct{})}
	go s.reap()
	return s, nil
}

// Close ends every shell and tunnel and stops the reaper. Idempotent.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	for _, sh := range s.shells {
		sh.close()
	}
	for _, t := range s.tunnels {
		t.close()
	}
	s.mu.Unlock()
	_ = s.root.Close()
}

func (s *Service) alive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// HostView is the agent-safe host summary: no credential refs or key data.
type HostView struct {
	Alias   string   `json:"alias"`
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	User    string   `json:"user"`
	Sudo    string   `json:"sudo"`
	Read    bool     `json:"read"`
	Write   bool     `json:"write"`
	Shell   bool     `json:"shell"`
	Tunnels []string `json:"tunnels,omitempty"`
	Timeout int      `json:"timeout_seconds"`
}

func (s *Service) Hosts() []HostView {
	out := make([]HostView, 0, len(s.cfg.Hosts))
	for alias, h := range s.cfg.Hosts {
		names := make([]string, 0, len(h.Tunnels))
		for name := range h.Tunnels {
			names = append(names, name)
		}
		slices.Sort(names)
		sudo := h.Sudo
		if sudo == "" {
			sudo = "never"
		}
		out = append(out, HostView{Alias: alias, Host: h.Host, Port: h.port(), User: h.User, Sudo: sudo,
			Read: len(h.Commands)+len(h.Paths) > 0, Write: s.grants.Write && len(h.WriteCommands)+len(h.WritePaths) > 0,
			Shell: s.grants.Shell, Tunnels: names, Timeout: int(h.timeout().Seconds())})
	}
	slices.SortFunc(out, func(a, b HostView) int { return strings.Compare(a.Alias, b.Alias) })
	return out
}

func (s *Service) host(alias string) (*Host, error) {
	h, ok := s.cfg.Hosts[alias]
	if !ok {
		s.auditEvent("ssh", alias, "denied", "unknown alias")
		return nil, fmt.Errorf("%w: unknown host alias %q", ErrNotFound, alias)
	}
	return h, nil
}

// dial authenticates with the pinned host key set. Every call dials fresh;
// there is no cached client to go stale between calls.
func (s *Service) dial(ctx context.Context, alias string, h *Host) (*ssh.Client, error) {
	signers, cleanup, err := s.signers(ctx, alias, h)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{User: h.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signers...)}, HostKeyCallback: h.hostKeyCallback(), Timeout: h.dialTimeout(), HostKeyAlgorithms: h.hostKeyAlgorithms()}
	dialer := net.Dialer{Timeout: h.dialTimeout()}
	conn, err := dialer.DialContext(ctx, "tcp", h.address())
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: dial: %v", ErrUnavailable, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, h.address(), config)
	if err != nil {
		_ = conn.Close()
		cleanup()
		return nil, fmt.Errorf("%w: handshake: %v", ErrUnavailable, err)
	}
	cleanup() // lease released; parsed signer lives on in memory
	return ssh.NewClient(c, chans, reqs), nil
}

func (h *Host) hostKeyCallback() ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		for _, pinned := range h.pinned {
			if bytes.Equal(pinned.Marshal(), key.Marshal()) {
				return nil
			}
		}
		fp := strings.TrimPrefix(ssh.FingerprintSHA256(key), "SHA256:")
		if h.sha256[strings.ToUpper(fp)] {
			return nil
		}
		return fmt.Errorf("%w: host key is not pinned", ErrDenied)
	}
}

// hostKeyAlgorithms restricts offered algorithms to the pinned key types so a
// rogue server cannot downgrade to a key type the owner did not pin.
func (h *Host) hostKeyAlgorithms() []string {
	out := make([]string, 0, len(h.pinned))
	seen := map[string]bool{}
	for _, key := range h.pinned {
		if name := key.Type(); !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 && len(h.sha256) > 0 {
		return nil // fingerprint pinning: accept any negotiated host key type
	}
	return out
}

// acquire takes the global concurrency slot or fails fast.
func (s *Service) acquire() (func(), error) {
	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	default:
		return nil, ErrBusy
	}
}

// auditEvent appends a bounded "ssh" event; op names the operation so the
// ledger stays filterable without new kinds. Audit failures never unblock a
// call.
func (s *Service) auditEvent(op, alias, outcome, receipt string) {
	if s.ledger == nil {
		return
	}
	event := audit.NewEvent("ssh", s.auth.PrincipalID, outcome)
	event.Name = alias
	event.ContextID = s.auth.ContextID
	event.RuntimeID = s.auth.RuntimeID
	event.PolicyRevision = s.auth.PolicyVersion
	receipt = op + " " + receipt
	if len(receipt) > 240 {
		receipt = receipt[:240]
	}
	event.Receipt = receipt
	_ = s.ledger.Append(event)
}

// commandPolicy validates and classifies a remote command. It returns the
// effective command (sudo prefix stripped) and whether the write grant is
// required. Denials are audit-visible.
func (s *Service) commandPolicy(h *Host, alias, command string) (string, bool, error) {
	command = strings.TrimSpace(command)
	if command == "" || len(command) > 4096 || strings.ContainsAny(command, "\r\n\x00") {
		return "", false, fmt.Errorf("%w: command must be a single line under 4 KiB", ErrInvalid)
	}
	effective := command
	sudo := strings.HasPrefix(command, "sudo ") || strings.HasPrefix(command, "sudo\t")
	if sudo {
		if h.Sudo != "passwordless" {
			s.auditEvent("ssh-exec", alias, "denied", "sudo not permitted")
			return "", false, fmt.Errorf("%w: sudo is not permitted on %q", ErrDenied, alias)
		}
		effective = strings.TrimSpace(command[len("sudo "):])
		if strings.HasPrefix(effective, "-n ") {
			effective = strings.TrimSpace(effective[3:])
		}
	}
	if matchAny(h.Commands, effective) {
		return effective, false, nil
	}
	if matchAny(h.WriteCommands, effective) {
		if !s.grants.Write {
			s.auditEvent("ssh-exec", alias, "denied", "write grant required")
			return "", false, fmt.Errorf("%w: command requires the ssh_write grant", ErrDenied)
		}
		return effective, true, nil
	}
	s.auditEvent("ssh-exec", alias, "denied", "command not allowlisted")
	return "", false, fmt.Errorf("%w: command is not allowlisted on %q", ErrDenied, alias)
}

// pathPolicy checks a remote path against an allowlist.
func (s *Service) pathPolicy(h *Host, alias, kind string, patterns []string, p string) error {
	if p == "" || len(p) > 4096 || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\r\n\x00") {
		return fmt.Errorf("%w: path must be absolute and single-line", ErrInvalid)
	}
	if !matchAny(patterns, p) {
		s.auditEvent("ssh-file", alias, "denied", kind+" path not allowlisted")
		return fmt.Errorf("%w: path is not allowlisted on %q", ErrDenied, alias)
	}
	return nil
}

func newID(prefix string) string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return prefix + "-unavailable"
	}
	return prefix + "-" + hex.EncodeToString(raw)
}
