// Package hosting provides owner-bound immutable site publication and serving.
package hosting

import (
	"errors"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const metadataReserve = (4 << 20) + (64 << 10)

var (
	ErrDenied      = errors.New("hosting denied")
	ErrInvalid     = errors.New("invalid hosting request")
	ErrConflict    = errors.New("hosting conflict")
	ErrQuota       = errors.New("hosting quota exceeded")
	ErrUnavailable = errors.New("hosting unavailable")
	ErrNotFound    = errors.New("site not found")
	dnsLabel       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	label          = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)
	id             = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
)

type Binding struct {
	Owner       string `json:"owner"`
	Environment string `json:"environment"`
}
type Config struct {
	AdmissionDigest string   `json:"admission_digest,omitempty"`
	Restart         string   `json:"restart,omitempty"`
	Slots           []string `json:"-"`
	Owner           string   `json:"owner"`
	Environment     string   `json:"environment"`
	Zone            string   `json:"zone"`
	Domain          string   `json:"domain"`
	Workspace       string   `json:"workspace"`
	Store           string   `json:"store"`
	MaxSites        int      `json:"max_sites"`
	MaxDynamic      int      `json:"max_dynamic"`
	MaxBytes        int64    `json:"max_bytes"`
	MaxReleaseBytes int64    `json:"max_release_bytes"`
	MaxFiles        int      `json:"max_files"`
	AuditBytes      int64    `json:"audit_bytes"`
	Reserved        []string `json:"reserved"`
}

func (c *Config) validate() error {
	if !id.MatchString(c.Owner) || (c.Environment != "dev" && c.Environment != "prod") || !label.MatchString(c.Zone) || c.Zone != c.Owner || !(filepath.IsAbs(c.Workspace) || path.IsAbs(c.Workspace)) || !(filepath.IsAbs(c.Store) || path.IsAbs(c.Store)) || c.Workspace == c.Store {
		return ErrInvalid
	}
	if relative, err := filepath.Rel(c.Workspace, c.Store); err == nil && filepath.IsLocal(relative) {
		return ErrInvalid
	}
	if relative, err := filepath.Rel(c.Store, c.Workspace); err == nil && filepath.IsLocal(relative) {
		return ErrInvalid
	}
	if len(c.Domain) > 200 || strings.ToLower(c.Domain) != c.Domain || net.ParseIP(c.Domain) != nil || !strings.Contains(c.Domain, ".") {
		return ErrInvalid
	}
	for _, part := range strings.Split(c.Domain, ".") {
		if !dnsLabel.MatchString(part) {
			return ErrInvalid
		}
	}
	if c.Restart == "" {
		c.Restart = "on-failure"
	}
	if c.Restart != "on-failure" && c.Restart != "never" {
		return ErrInvalid
	}
	if c.MaxSites == 0 {
		c.MaxSites = 4
	}
	if c.MaxDynamic == 0 {
		c.MaxDynamic = 2
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 512 << 20
	}
	if c.MaxReleaseBytes == 0 {
		c.MaxReleaseBytes = 128 << 20
	}
	if c.MaxFiles == 0 {
		c.MaxFiles = 10000
	}
	if c.AuditBytes == 0 {
		c.AuditBytes = 64 << 20
	}
	if c.MaxSites < 1 || c.MaxSites > 16 || c.MaxDynamic < 0 || c.MaxDynamic > c.MaxSites || c.MaxBytes <= metadataReserve || c.MaxReleaseBytes < 1 || c.MaxReleaseBytes > c.MaxBytes-metadataReserve || c.MaxFiles < 1 || c.MaxFiles > 100000 || c.AuditBytes < int64((c.MaxSites+1)*2048) || c.AuditBytes > 64<<20 {
		return ErrInvalid
	}
	return nil
}
func safePath(p string) bool {
	if p == "." {
		return true
	}
	if !filepath.IsLocal(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:\x00") {
		return false
	}
	for _, v := range strings.Split(p, "/") {
		if !safeFile(v) {
			return false
		}
	}
	return true
}
func safeFile(p string) bool {
	lower := strings.ToLower(p)
	if lower == "config" || strings.HasPrefix(lower, "config.") || lower == "agent.yaml" || lower == "settings.yaml" || lower == "workspace.yaml" {
		return false
	}
	return p != "" && !strings.HasPrefix(p, ".") && !strings.ContainsAny(p, "\\:\x00") && p != ".." && !strings.HasSuffix(lower, ".key") && !strings.HasSuffix(lower, ".pem") && !strings.HasPrefix(lower, "credentials") && !strings.HasPrefix(lower, "secrets") && lower != "token.json" && lower != "tokens.json"
}
func (c Config) origin(name string) string { return "https://" + name + "." + c.Zone + "." + c.Domain }
func (c Config) validName(name string) bool {
	if len(c.Slots) > 0 {
		found := false
		for _, slot := range c.Slots {
			if slot == name {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(name)+len(c.Zone)+len(c.Domain)+2 > 253 {
		return false
	}
	if !label.MatchString(name) {
		return false
	}
	for _, v := range append([]string{"api", "admin", "hub", "auth", "broker", "control", "localhost"}, c.Reserved...) {
		if name == v {
			return false
		}
	}
	return true
}

type PublishRequest struct {
	Name             string `json:"name"`
	Source           string `json:"source"`
	Mode             string `json:"mode,omitempty"`
	Access           string `json:"access,omitempty"`
	Profile          string `json:"profile,omitempty"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestID        string `json:"request_id"`
	LifetimeSeconds  int    `json:"lifetime_seconds,omitempty"`
}
type UnpublishRequest struct {
	Name             string `json:"name"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestID        string `json:"request_id"`
}
type Site struct {
	Name      string    `json:"name"`
	Revision  uint64    `json:"revision"`
	State     string    `json:"state"`
	Mode      string    `json:"mode"`
	Access    string    `json:"access"`
	Origin    string    `json:"origin"`
	ExpiresAt time.Time `json:"expires_at"`
	URL       string    `json:"url,omitempty"`
}
type record struct {
	AdmissionDigest string    `json:"admission_digest,omitempty"`
	Restarts        int       `json:"restarts"`
	NextRestart     time.Time `json:"next_restart"`
	Site
	Release      string    `json:"release"`
	TokenHash    string    `json:"token_hash,omitempty"`
	ShareExpires time.Time `json:"share_expires"`
	Bytes        int64     `json:"bytes"`
	Files        int       `json:"files"`
}
type operation struct {
	Digest string `json:"digest"`
	Result Site   `json:"result"`
}
type diskState struct {
	Sequences  map[string]uint64    `json:"sequences"`
	Pending    map[string]record    `json:"pending"`
	Retired    map[string]record    `json:"retired"`
	Schema     int                  `json:"schema"`
	Binding    Binding              `json:"binding"`
	Sites      map[string]record    `json:"sites"`
	Operations map[string]operation `json:"operations"`
}
