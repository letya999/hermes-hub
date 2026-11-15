package stack

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/providers/hindsight"
)

// providerConfig is the rendered operator-owned endpoint record inside the
// vault's hub-owned sidecar zone (SPEC-0085 R11.2). It carries the adapter
// kind and URL only — the credential stays behind a secrets name so no
// material lands in the space tree, and no scope material either: replay
// mints bank ids from runtime/memory/scope.key.
type providerConfig struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	TokenEnv string `json:"token_env,omitempty"`
}

// providerEnabled reports the fully gated capability: the tools wish alone
// is inert until governance admits native:memory_provider — deny-wins like
// every hub-side flag (knowledge_ui precedent).
func (s Settings) providerEnabled() bool {
	return s.Has("memory_provider") && s.govAllows("native:memory_provider")
}

// providerConfigPath is the rendered record's home — inside the vault's
// runtime/ sidecar zone, ignored by .okignore and outside every governed
// memory path so space content can never forge provider config.
func providerConfigPath(dir string) string {
	return filepath.Join(dir, "vault", "runtime", "memory", "provider.json")
}

// renderProviderConfig materializes the endpoint record when the capability
// is granted and removes it otherwise — disabling the capability revokes
// the rendered config so nothing stale can drive a later replay.
func renderProviderConfig(dir string, s Settings) error {
	path := providerConfigPath(dir)
	if !s.providerEnabled() {
		return removeIfExists(path)
	}
	cfg, err := json.Marshal(providerConfig{
		Provider: s.MemoryProvider,
		URL:      s.MemoryProviderURL,
		TokenEnv: s.MemoryProviderTokenEnv,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomic(path, append(cfg, '\n'))
}

// validateProviderConfig enforces the field-level contract an enabled wish
// relies on: a known adapter kind, an absolute http(s) endpoint and a
// token field that names a secrets variable — an unknown kind, an orphan
// field or a literal credential all fail validation, never a silent
// fallback (SPEC-0085 R11.1/R11.4).
func (s Settings) validateProviderConfig() error {
	hasFields := s.MemoryProvider != "" || s.MemoryProviderURL != "" || s.MemoryProviderTokenEnv != ""
	if !hasFields {
		if s.Has("memory_provider") {
			return fmt.Errorf("memory_provider capability requires the memory_provider and memory_provider_url fields")
		}
		return nil
	}
	if !s.Has("memory_provider") {
		return fmt.Errorf("memory_provider fields require the memory_provider capability")
	}
	if s.MemoryProvider == "" || s.MemoryProviderURL == "" {
		return fmt.Errorf("memory_provider capability requires the memory_provider and memory_provider_url fields")
	}
	if s.MemoryProvider != "hindsight" {
		return fmt.Errorf("memory_provider %q is not a supported adapter (supported: hindsight)", s.MemoryProvider)
	}
	u, err := url.Parse(s.MemoryProviderURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("memory_provider_url %q is not an absolute http(s) endpoint", s.MemoryProviderURL)
	}
	for _, r := range s.MemoryProviderTokenEnv {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("memory_provider_token_env %q must name a secrets variable, not a value", s.MemoryProviderTokenEnv)
		}
	}
	return nil
}

// OpenProvider instantiates the rendered provider for operator-facing lanes
// (replay, diagnostics). The rendered record alone decides the adapter; the
// credential resolves from the space's secrets env by name — never a literal
// value in config (SPEC-0085 R11.4).
func OpenProvider(dir string, s Settings) (memory.Provider, error) {
	body, err := os.ReadFile(providerConfigPath(dir))
	if err != nil {
		return nil, fmt.Errorf("memory provider config not rendered — is the capability granted? %w", err)
	}
	var cfg providerConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("memory provider config corrupt: %w", err)
	}
	token := ""
	if cfg.TokenEnv != "" {
		envFile := "runtime." + s.Environment + ".env"
		if s.CapabilityMode == "managed" {
			envFile = "managed-runtime." + s.Environment + ".env"
		}
		secrets, err := ReadSecrets(filepath.Join(dir, envFile))
		if err != nil {
			return nil, fmt.Errorf("memory provider secrets: %w", err)
		}
		token = strings.TrimSpace(secrets[cfg.TokenEnv])
		if token == "" {
			return nil, fmt.Errorf("memory provider token: %s unset in %s", cfg.TokenEnv, envFile)
		}
	}
	switch cfg.Provider {
	case "hindsight":
		return hindsight.New(cfg.URL, hindsight.WithToken(token))
	default:
		return nil, fmt.Errorf("memory provider %q is not a supported adapter", cfg.Provider)
	}
}

// providerEgressHost extracts the endpoint's host for the managed egress
// allowlist — only ever invoked on a validated absolute http(s) URL.
func providerEgressHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
