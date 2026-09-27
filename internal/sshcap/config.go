// Package sshcap is the hub-owned opt-in SSH capability. Host, account, port
// and host keys are bound in a read-only per-owner config; the agent only
// names an alias. Read/diagnostic effects are the default; write, interactive
// shell and managed tunnels are separate grants.
package sshcap

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

var (
	ErrInvalid     = errors.New("invalid ssh capability config")
	ErrDenied      = errors.New("ssh capability denied")
	ErrNotFound    = errors.New("ssh capability target not found")
	ErrUnavailable = errors.New("ssh capability unavailable")
	ErrClosed      = errors.New("ssh capability is closed")
	ErrBusy        = errors.New("ssh capability concurrency limit reached")
)

const (
	maxConfigBytes    = 128 << 10
	defaultPort       = 22
	defaultTimeout    = 30
	maxTimeout        = 300
	defaultDial       = 10
	maxDial           = 60
	defaultOutput     = 64 << 10
	maxOutput         = 1 << 20
	defaultRead       = 1 << 20
	maxRead           = 16 << 20
	maxSessions       = 8
	maxTunnels        = 8
	maxShells         = 4
	writeLimit        = 2 << 20
	shellBuffer       = 256 << 10
	defaultShellIdle  = 600
	maxShellIdle      = 3600
	defaultShellTTL   = 1800
	maxShellTTL       = 4 * 3600
	defaultTunnelIdle = 300
	maxTunnelIdle     = 3600
	defaultTunnelTTL  = 3600
	maxTunnelTTL      = 8 * 3600
)

var (
	aliasPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	userPattern    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}$`)
	tunnelPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	grantIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]{4,128}$`)
)

type Config struct {
	Schema   int              `yaml:"schema"`
	Defaults Defaults         `yaml:"defaults,omitempty"`
	Hosts    map[string]*Host `yaml:"hosts"`
}

type Defaults struct {
	MaxSessions    int `yaml:"max_sessions,omitempty"`
	MaxTunnels     int `yaml:"max_tunnels,omitempty"`
	MaxShells      int `yaml:"max_shells,omitempty"`
	ShellIdle      int `yaml:"shell_idle_seconds,omitempty"`
	ShellLifetime  int `yaml:"shell_lifetime_seconds,omitempty"`
	TunnelIdle     int `yaml:"tunnel_idle_seconds,omitempty"`
	TunnelLifetime int `yaml:"tunnel_lifetime_seconds,omitempty"`
}

type Host struct {
	Host           string              `yaml:"host"`
	Port           int                 `yaml:"port,omitempty"`
	User           string              `yaml:"user"`
	HostKeys       []string            `yaml:"host_keys"`
	KeyRef         string              `yaml:"key_ref"`
	CertificateRef string              `yaml:"certificate_ref,omitempty"`
	Commands       []string            `yaml:"commands,omitempty"`
	WriteCommands  []string            `yaml:"write_commands,omitempty"`
	Paths          []string            `yaml:"paths,omitempty"`
	WritePaths     []string            `yaml:"write_paths,omitempty"`
	Sudo           string              `yaml:"sudo,omitempty"`
	Timeout        int                 `yaml:"timeout_seconds,omitempty"`
	DialTimeout    int                 `yaml:"dial_timeout_seconds,omitempty"`
	MaxOutput      int                 `yaml:"max_output_bytes,omitempty"`
	MaxRead        int                 `yaml:"max_read_bytes,omitempty"`
	Tunnels        map[string]*Forward `yaml:"tunnels,omitempty"`

	pinned []ssh.PublicKey
	sha256 map[string]bool
}

type Forward struct {
	RemoteHost string `yaml:"remote_host"`
	RemotePort int    `yaml:"remote_port"`
}

// Load reads and validates the per-owner SSH config at path. The file must be
// a regular file under a directory the caller already trusts as owner-scoped.
func Load(path string) (*Config, error) {
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxConfigBytes {
		return nil, fmt.Errorf("%w: config must be a regular file under %d bytes", ErrInvalid, maxConfigBytes)
	}
	body, err := os.ReadFile(clean) // #nosec G304 -- operator-owned config path.
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	cfg := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err = dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: one YAML document expected", ErrInvalid)
	}
	if err = cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Schema != 1 {
		return fmt.Errorf("%w: schema must be 1", ErrInvalid)
	}
	if len(c.Hosts) == 0 || len(c.Hosts) > 64 {
		return fmt.Errorf("%w: 1-64 hosts required", ErrInvalid)
	}
	for _, d := range []struct {
		v, lo, hi int
	}{
		{c.Defaults.MaxSessions, 0, maxSessions}, {c.Defaults.MaxTunnels, 0, maxTunnels},
		{c.Defaults.MaxShells, 0, maxShells}, {c.Defaults.ShellIdle, 0, maxShellIdle},
		{c.Defaults.ShellLifetime, 0, maxShellTTL}, {c.Defaults.TunnelIdle, 0, maxTunnelIdle},
		{c.Defaults.TunnelLifetime, 0, maxTunnelTTL},
	} {
		if d.v < d.lo || d.v > d.hi {
			return fmt.Errorf("%w: defaults bound exceeded", ErrInvalid)
		}
	}
	for alias, h := range c.Hosts {
		if h == nil || !aliasPattern.MatchString(alias) {
			return fmt.Errorf("%w: invalid host alias %q", ErrInvalid, alias)
		}
		if err := h.validate(alias); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) validate(alias string) error {
	if !hostPattern.MatchString(h.Host) && net.ParseIP(h.Host) == nil {
		return fmt.Errorf("%w: host %q must be a hostname or IP", ErrInvalid, alias)
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("%w: host %q invalid port", ErrInvalid, alias)
	}
	if !userPattern.MatchString(h.User) {
		return fmt.Errorf("%w: host %q invalid user", ErrInvalid, alias)
	}
	if len(h.HostKeys) == 0 || len(h.HostKeys) > 8 {
		return fmt.Errorf("%w: host %q requires 1-8 pinned host_keys", ErrInvalid, alias)
	}
	h.pinned = nil
	h.sha256 = map[string]bool{}
	for _, entry := range h.HostKeys {
		entry = strings.TrimSpace(entry)
		if fp, ok := strings.CutPrefix(entry, "sha256:"); ok {
			fp = strings.TrimPrefix(fp, "SHA256:")
			if len(fp) < 16 {
				return fmt.Errorf("%w: host %q invalid sha256 host key", ErrInvalid, alias)
			}
			h.sha256[strings.ToUpper(fp)] = true
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(entry))
		if err != nil {
			return fmt.Errorf("%w: host %q invalid pinned host key", ErrInvalid, alias)
		}
		h.pinned = append(h.pinned, key)
	}
	for _, ref := range []string{h.KeyRef, h.CertificateRef} {
		if ref == "" {
			continue
		}
		scheme, rest, ok := strings.Cut(ref, ":")
		if !ok || (scheme != "file" && scheme != "broker") {
			return fmt.Errorf("%w: host %q credential ref must be file: or broker: scheme", ErrInvalid, alias)
		}
		if scheme == "file" {
			if rest == "" || !fs.ValidPath(rest) || strings.HasPrefix(rest, "/") || strings.Contains(rest, "\\") {
				return fmt.Errorf("%w: host %q file credential ref must stay inside the config dir", ErrInvalid, alias)
			}
		} else if !grantIDPattern.MatchString(rest) {
			return fmt.Errorf("%w: host %q invalid broker grant id", ErrInvalid, alias)
		}
	}
	if h.KeyRef == "" {
		return fmt.Errorf("%w: host %q requires key_ref", ErrInvalid, alias)
	}
	switch h.Sudo {
	case "", "never", "passwordless":
	default:
		return fmt.Errorf("%w: host %q sudo must be never or passwordless", ErrInvalid, alias)
	}
	for _, list := range [][]string{h.Commands, h.WriteCommands} {
		for _, pattern := range list {
			if strings.TrimSpace(pattern) == "" || strings.ContainsAny(pattern, "\r\n\x00") || len(pattern) > 4096 {
				return fmt.Errorf("%w: host %q invalid command pattern", ErrInvalid, alias)
			}
		}
	}
	for _, list := range [][]string{h.Paths, h.WritePaths} {
		for _, pattern := range list {
			if !strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\r\n\x00") || len(pattern) > 4096 {
				return fmt.Errorf("%w: host %q path patterns must be absolute", ErrInvalid, alias)
			}
		}
	}
	if len(h.Commands)+len(h.WriteCommands)+len(h.Paths)+len(h.WritePaths)+len(h.Tunnels) == 0 {
		return fmt.Errorf("%w: host %q grants no capability", ErrInvalid, alias)
	}
	if h.Timeout < 0 || h.Timeout > maxTimeout || h.DialTimeout < 0 || h.DialTimeout > maxDial {
		return fmt.Errorf("%w: host %q timeout bound exceeded", ErrInvalid, alias)
	}
	if h.MaxOutput < 0 || h.MaxOutput > maxOutput || h.MaxRead < 0 || h.MaxRead > maxRead {
		return fmt.Errorf("%w: host %q output bound exceeded", ErrInvalid, alias)
	}
	for name, fwd := range h.Tunnels {
		if fwd == nil || !tunnelPattern.MatchString(name) {
			return fmt.Errorf("%w: host %q invalid tunnel name %q", ErrInvalid, alias, name)
		}
		if !hostPattern.MatchString(fwd.RemoteHost) && net.ParseIP(fwd.RemoteHost) == nil {
			return fmt.Errorf("%w: host %q tunnel %q invalid remote_host", ErrInvalid, alias, name)
		}
		if fwd.RemotePort < 1 || fwd.RemotePort > 65535 {
			return fmt.Errorf("%w: host %q tunnel %q invalid remote_port", ErrInvalid, alias, name)
		}
	}
	return nil
}

func (h *Host) address() string { return net.JoinHostPort(h.Host, fmt.Sprint(h.port())) }
func (h *Host) port() int {
	if h.Port == 0 {
		return defaultPort
	}
	return h.Port
}
func (h *Host) timeout() time.Duration {
	if h.Timeout == 0 {
		return defaultTimeout * time.Second
	}
	return time.Duration(h.Timeout) * time.Second
}
func (h *Host) dialTimeout() time.Duration {
	if h.DialTimeout == 0 {
		return defaultDial * time.Second
	}
	return time.Duration(h.DialTimeout) * time.Second
}
func (h *Host) outputLimit() int64 {
	if h.MaxOutput == 0 {
		return defaultOutput
	}
	return int64(h.MaxOutput)
}
func (h *Host) readLimit() int64 {
	if h.MaxRead == 0 {
		return defaultRead
	}
	return int64(h.MaxRead)
}
