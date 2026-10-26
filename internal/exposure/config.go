// Package exposure implements the named outbound-exposure primitive
// (ADR-0079/SPEC-0077): a reviewed per-owner config declares named endpoints
// that map a public URL to a declared internal target through an
// outbound-only tunnel workload. The agent only ever names an endpoint.
package exposure

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
	ErrInvalid     = errors.New("invalid exposure config")
	ErrUnavailable = errors.New("exposure config unavailable")
	ErrNotFound    = errors.New("exposure endpoint not found")
	ErrDenied      = errors.New("exposure denied")
	ErrBusy        = errors.New("exposure concurrency limit reached")
)

const (
	maxConfigBytes  = 64 << 10
	maxEndpoints    = 16
	defaultMax      = 4
	maxMax          = 8
	defaultIdle     = 1800
	maxIdle         = 24 * 3600
	defaultLifetime = 8 * 3600
	maxLifetime     = 7 * 24 * 3600
	defaultPort     = 22
)

var (
	endpointPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	labelPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	userPattern     = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}$`)
	servicePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	grantIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9:_-]{4,128}$`)
)

// TargetHubListener is the reserved endpoint target resolving to the
// restricted public-listener surface (ADR-0054 D2).
const TargetHubListener = "hub_public_listener"

type Config struct {
	Schema          int                  `yaml:"schema"`
	Provider        string               `yaml:"provider"`
	Relay           *Relay               `yaml:"relay,omitempty"`
	Zone            string               `yaml:"zone,omitempty"`
	KeyRef          string               `yaml:"key_ref"`
	MaxExposures    int                  `yaml:"max_exposures,omitempty"`
	IdleSeconds     int                  `yaml:"idle_seconds,omitempty"`
	LifetimeSeconds int                  `yaml:"lifetime_seconds,omitempty"`
	Endpoints       map[string]*Endpoint `yaml:"endpoints"`
}

// Relay describes the sish server (operator VPS) the workload dials.
type Relay struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port,omitempty"`
	User     string   `yaml:"user"`
	Domain   string   `yaml:"domain"`
	HostKeys []string `yaml:"host_keys"`

	pinned []ssh.PublicKey
	sha256 map[string]bool
}

type Endpoint struct {
	Target string `yaml:"target"`
}

// Load reads and validates the per-owner exposure config at path.
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
	switch c.Provider {
	case "sish":
		if c.Relay == nil {
			return fmt.Errorf("%w: sish requires relay", ErrInvalid)
		}
		if err := c.Relay.validate(); err != nil {
			return err
		}
		if !labelPattern.MatchString(c.Zone) {
			return fmt.Errorf("%w: sish requires a zone DNS label", ErrInvalid)
		}
	case "cloudflared":
		if c.Relay != nil || c.Zone != "" {
			return fmt.Errorf("%w: cloudflared forbids relay/zone — names are account-side", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: provider must be sish or cloudflared", ErrInvalid)
	}
	if err := validateKeyRef(c.KeyRef); err != nil {
		return err
	}
	if c.MaxExposures < 0 || c.MaxExposures > maxMax {
		return fmt.Errorf("%w: max_exposures bound 1-%d", ErrInvalid, maxMax)
	}
	if c.IdleSeconds < 0 || c.IdleSeconds > maxIdle || c.LifetimeSeconds < 0 || c.LifetimeSeconds > maxLifetime {
		return fmt.Errorf("%w: idle/lifetime bound exceeded", ErrInvalid)
	}
	if len(c.Endpoints) == 0 || len(c.Endpoints) > maxEndpoints {
		return fmt.Errorf("%w: 1-%d endpoints required", ErrInvalid, maxEndpoints)
	}
	for name, ep := range c.Endpoints {
		if ep == nil || !endpointPattern.MatchString(name) {
			return fmt.Errorf("%w: invalid endpoint name %q", ErrInvalid, name)
		}
		if err := validateTarget(ep.Target); err != nil {
			return fmt.Errorf("%w: endpoint %q %v", ErrInvalid, name, err)
		}
	}
	return nil
}

func (r *Relay) validate() error {
	if !hostPattern.MatchString(r.Host) && net.ParseIP(r.Host) == nil {
		return fmt.Errorf("%w: relay host must be a hostname or IP", ErrInvalid)
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("%w: relay port invalid", ErrInvalid)
	}
	if !userPattern.MatchString(r.User) {
		return fmt.Errorf("%w: relay user invalid", ErrInvalid)
	}
	if !hostPattern.MatchString(r.Domain) || !strings.Contains(r.Domain, ".") {
		return fmt.Errorf("%w: relay domain must be a qualified DNS name", ErrInvalid)
	}
	if len(r.HostKeys) == 0 || len(r.HostKeys) > 8 {
		return fmt.Errorf("%w: relay requires 1-8 pinned host_keys", ErrInvalid)
	}
	r.pinned = nil
	r.sha256 = map[string]bool{}
	for _, entry := range r.HostKeys {
		entry = strings.TrimSpace(entry)
		if fp, ok := strings.CutPrefix(entry, "sha256:"); ok {
			fp = strings.TrimPrefix(fp, "SHA256:")
			if len(fp) < 16 {
				return fmt.Errorf("%w: invalid sha256 host key", ErrInvalid)
			}
			r.sha256[strings.ToUpper(fp)] = true
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(entry))
		if err != nil {
			return fmt.Errorf("%w: invalid pinned relay host key", ErrInvalid)
		}
		r.pinned = append(r.pinned, key)
	}
	return nil
}

func validateKeyRef(ref string) error {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok || (scheme != "file" && scheme != "broker") {
		return fmt.Errorf("%w: key_ref must be file: or broker: scheme", ErrInvalid)
	}
	if scheme == "file" {
		if rest == "" || !fs.ValidPath(rest) || strings.HasPrefix(rest, "/") || strings.Contains(rest, "\\") {
			return fmt.Errorf("%w: file key_ref must stay inside the config dir", ErrInvalid)
		}
	} else if !grantIDPattern.MatchString(rest) {
		return fmt.Errorf("%w: invalid broker grant id", ErrInvalid)
	}
	return nil
}

// validateTarget accepts the reserved public listener, a named
// service:<name>:<port>, or a literal host:port — all owner-declared.
func validateTarget(target string) error {
	if target == TargetHubListener {
		return nil
	}
	if svc, ok := strings.CutPrefix(target, "service:"); ok {
		name, port, ok := strings.Cut(svc, ":")
		if !ok || !servicePattern.MatchString(name) {
			return errors.New("invalid service target")
		}
		return validatePort(port)
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("target must be hub_public_listener, service:<name>:<port> or host:port")
	}
	if !hostPattern.MatchString(host) && net.ParseIP(host) == nil {
		return errors.New("invalid target host")
	}
	return validatePort(port)
}

func validatePort(s string) error {
	var p int
	if _, err := fmt.Sscanf(s, "%d", &p); err != nil || p < 1 || p > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

// EndpointFor resolves an endpoint name; unknown names fail closed.
func (c *Config) EndpointFor(name string) (*Endpoint, error) {
	ep, ok := c.Endpoints[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return ep, nil
}

// BindName is the sish -R bind for an endpoint: <endpoint>.<zone> under the
// relay domain. Composed from validated fields only — never caller input.
func (c *Config) BindName(name string) (string, error) {
	if c.Provider != "sish" {
		return "", fmt.Errorf("%w: bind names exist only on sish", ErrInvalid)
	}
	if _, err := c.EndpointFor(name); err != nil {
		return "", err
	}
	return name + "." + c.Zone, nil
}

// PublicName is the endpoint's public hostname on the sish profile.
func (c *Config) PublicName(name string) (string, error) {
	bind, err := c.BindName(name)
	if err != nil {
		return "", err
	}
	return bind + "." + c.Relay.Domain, nil
}

func (c *Config) Max() int {
	if c.MaxExposures == 0 {
		return defaultMax
	}
	return c.MaxExposures
}

func (c *Config) Idle() time.Duration {
	if c.IdleSeconds == 0 {
		return defaultIdle * time.Second
	}
	return time.Duration(c.IdleSeconds) * time.Second
}

func (c *Config) Lifetime() time.Duration {
	if c.LifetimeSeconds == 0 {
		return defaultLifetime * time.Second
	}
	return time.Duration(c.LifetimeSeconds) * time.Second
}

func (r *Relay) Address() string {
	port := r.Port
	if port == 0 {
		port = defaultPort
	}
	return net.JoinHostPort(r.Host, fmt.Sprint(port))
}
