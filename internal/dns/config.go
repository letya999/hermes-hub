package dns

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the operator DNS profile (connections/dns/config.yaml). Zone is
// the provider-served zone the API writes to; Base is the name prefix all
// records live under (default = Zone; a delegated profile has Base == Zone).
type Config struct {
	Schema         int      `yaml:"schema"`
	Provider       string   `yaml:"provider"`
	Zone           string   `yaml:"zone"`
	Base           string   `yaml:"base,omitempty"`
	CredentialRef  string   `yaml:"credential_ref"`
	NameServers    []string `yaml:"name_servers,omitempty"`
	StaticEgressIP string   `yaml:"static_egress_ip,omitempty"`
	Project        string   `yaml:"project,omitempty"`      // gcloud
	ManagedZone    string   `yaml:"managed_zone,omitempty"` // gcloud
}

// Load reads and validates the operator DNS profile at path.
func Load(path string) (*Config, error) {
	body, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- operator-owned config path.
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err = yaml.Unmarshal(body, cfg); err != nil {
		return nil, err
	}
	if err = cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Schema != 1 {
		return ErrInvalid
	}
	if c.Base == "" {
		c.Base = c.Zone
	}
	if c.Base != c.Zone && !strings.HasSuffix(c.Base, "."+c.Zone) {
		return ErrInvalid
	}
	switch c.Provider {
	case "regru":
		// REG.API mandates a panel IP allowlist (SPEC-0076 R6): without a
		// declared static egress IP the profile is unreachable — fail closed.
		if net.ParseIP(c.StaticEgressIP) == nil {
			return ErrDenied
		}
	case "digitalocean":
	case "gcloud":
		if c.ManagedZone == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	scheme, _, ok := strings.Cut(c.CredentialRef, ":")
	if !ok || (scheme != "file" && scheme != "broker") {
		return ErrInvalid
	}
	return nil
}

var recordLabelRe = regexp.MustCompile(`^(\*|@|[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)$`)

// RecordName confines a relative record name to the base zone and returns
// the provider-side subdomain (relative to Zone). Escapes fail closed:
// only clean relative labels ("*.alice", "_acme-challenge.alice", "@") are
// accepted, and the result can never leave the configured zone.
func (c *Config) RecordName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: bad record name %q", ErrInvalid, name)
	}
	for _, label := range strings.Split(name, ".") {
		if !recordLabelRe.MatchString(label) {
			return "", fmt.Errorf("%w: bad record label in %q", ErrInvalid, name)
		}
	}
	if c.Base == c.Zone {
		return name, nil
	}
	return name + "." + strings.TrimSuffix(c.Base, "."+c.Zone), nil
}

// FQDN renders the absolute owner name for a relative record name.
func (c *Config) FQDN(name string) string {
	return name + "." + c.Base
}
