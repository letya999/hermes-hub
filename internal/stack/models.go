package stack

import (
	"errors"
	"fmt"
	brokeridentity "github.com/letya999/credential-broker/identity"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// ModelRegistry is the relay projection of one published models passport.
type ModelRegistry struct {
	RuntimeModel string                   `yaml:"runtime_model,omitempty"`
	Providers    map[string]ModelProvider `yaml:"providers"`
	Models       map[string]ModelEntry    `yaml:"models"`
	// Switch maps owner (or "*") to the model-switch grant: "none" keeps
	// automatic ladders only, "logical" allows granted registry names,
	// "catalog" allows any model the owner's accounts actually serve.
	// An absent entry resolves to "logical" at the relay.
	Switch map[string]string `yaml:"switch,omitempty"`
}

type ModelProvider struct {
	BrokerGrants  map[string]string `yaml:"broker_grants,omitempty"`
	CredentialEnv string            `yaml:"credential_env,omitempty"`
	Owners        []string          `yaml:"owners,omitempty"`
	AllowPrivate  bool              `yaml:"allow_private,omitempty"`
	URL           string            `yaml:"url"`
	KeyEnv        string            `yaml:"key_env,omitempty"`
	// Via and UpstreamKind are the passport service's transport and upstream
	// credential kind ("cliproxy"/"openai-compatible", "oauth-session"/
	// "broker-key") — the relay reads them to decide which limit kinds the
	// service's own telemetry can measure.
	Via          string `yaml:"via,omitempty"`
	UpstreamKind string `yaml:"upstream_kind,omitempty"`
}

func (p ModelProvider) Allows(owner string) bool {
	return (len(p.Owners) == 0 || slices.Contains(p.Owners, owner)) && (len(p.BrokerGrants) == 0 || p.BrokerGrants[owner] != "")
}

type ModelHopRef struct {
	Provider string       `yaml:"provider"`
	Model    string       `yaml:"model,omitempty"`
	Models   []string     `yaml:"models,omitempty"`
	Limits   []ModelLimit `yaml:"limits,omitempty"`
	// Reasoning declares the service accepts reasoning parameters; requests
	// carrying them skip services that never declared support.
	Reasoning bool `yaml:"reasoning,omitempty"`
	// Sharing declares who the credential principal serves: "shared" (one
	// principal for every admitted owner) or "personal" (per-owner
	// broker-granted material — a shared key_env cannot be personal).
	Sharing string `yaml:"sharing,omitempty"`
}

// ModelLimit is one durable quota window for the hop's credential principal:
// a fixed {of, amount, unit?, per, clock?} grammar. "requests" counts every
// upstream call, "tokens" reserves the conservative request estimate up
// front; "provider_quota" and "spend_usd" are declared but only measurable
// where the service reports per-session usage or a catalog price exists —
// the relay disables a hop whose limits it cannot measure. Counters live in
// the relay state directory and never cross spaces; scope "shared" meters
// the principal, "personal" meters each owner separately.
type ModelLimit struct {
	Of     string `yaml:"of"`
	Amount int64  `yaml:"amount"`
	Unit   string `yaml:"unit,omitempty"`
	Per    string `yaml:"per"`
	Clock  string `yaml:"clock,omitempty"`
	Scope  string `yaml:"scope,omitempty"`
}

type ModelEntry struct {
	Owners []string      `yaml:"owners,omitempty"`
	Ladder []ModelHopRef `yaml:"ladder"`
}

// Allows reports whether the owner may select this logical model; an empty
// owners list means every space in the deployment may use it.
func (e ModelEntry) Allows(owner string) bool {
	return len(e.Owners) == 0 || slices.Contains(e.Owners, owner)
}

type modelRegistryYAML struct {
	Providers map[string]ModelProvider `yaml:"providers"`
	Models    map[string]ModelEntry    `yaml:"models"`
	Switch    map[string]string        `yaml:"switch"`
}

var (
	registryNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	registryModelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	envNamePattern       = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// validateProviderURL accepts only fixed http(s) base URLs — no credentials,
// query, fragment or path traversal, same contract as relay hop specs.
func validateProviderURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "..") {
		return errors.New("needs a fixed HTTP(S) base URL")
	}
	return nil
}

// LoadModelRegistryFor reads only the validated effective models doc — the
// same projection the old published-file read produced, now from the
// contract's stored bundle (S7d).
func LoadModelRegistryFor(dir string, eff spawnv1.Effective) (*ModelRegistry, error) {
	layer, err := registryLayerFor(dir, eff)
	if err != nil {
		return nil, err
	}
	reg := &ModelRegistry{Providers: layer.Providers, Models: layer.Models, Switch: layer.Switch}
	return reg, reg.Validate()
}

// Validate applies the same provider/model contract at render and relay reload.
func (r *ModelRegistry) Validate() error {
	for name, p := range r.Providers {
		if !registryNamePattern.MatchString(name) {
			return fmt.Errorf("models.yaml: invalid provider name %q", name)
		}
		if err := validateProviderURL(p.URL); err != nil {
			return fmt.Errorf("models.yaml: provider %s: %w", name, err)
		}
		if p.KeyEnv != "" && !envNamePattern.MatchString(p.KeyEnv) {
			return fmt.Errorf("models.yaml: provider %s key_env is not an env name", name)
		}
		for _, owner := range p.Owners {
			if !idPattern.MatchString(owner) {
				return fmt.Errorf("models.yaml: provider %s has invalid owner scope", name)
			}
		}
		if p.BrokerGrants != nil {
			if len(p.BrokerGrants) == 0 || p.KeyEnv != "" || !envNamePattern.MatchString(p.CredentialEnv) {
				return fmt.Errorf("models.yaml: provider %s needs explicit grants and one credential source", name)
			}
			for owner, grant := range p.BrokerGrants {
				if !idPattern.MatchString(owner) || !brokeridentity.ValidID(grant) || (len(p.Owners) > 0 && !slices.Contains(p.Owners, owner)) {
					return fmt.Errorf("models.yaml: provider %s has invalid credential scope", name)
				}
			}
		}
	}
	for name, m := range r.Models {
		if !registryNamePattern.MatchString(name) {
			return fmt.Errorf("models.yaml: invalid model name %q", name)
		}
		if len(m.Ladder) == 0 || len(m.Ladder) > 8 {
			return fmt.Errorf("models.yaml: model %q needs one to eight fallback hops", name)
		}
		for _, owner := range m.Owners {
			if !idPattern.MatchString(owner) {
				return fmt.Errorf("models.yaml: model %q bad owner %q", name, owner)
			}
		}
		for _, hop := range m.Ladder {
			if _, ok := r.Providers[hop.Provider]; !ok && hop.Provider != "cliproxy" {
				return fmt.Errorf("models.yaml: model %q uses unknown provider %q", name, hop.Provider)
			}
			ids := hop.Models
			if len(ids) == 0 {
				ids = []string{hop.Model}
			} else if strings.TrimSpace(hop.Model) != "" {
				return fmt.Errorf("models.yaml: model %q sets both model and models", name)
			}
			for _, id := range ids {
				if strings.TrimSpace(id) == "" || !registryModelPattern.MatchString(id) {
					return fmt.Errorf("models.yaml: model %q bad provider model id %q", name, id)
				}
			}
			switch hop.Sharing {
			case "", "shared":
			case "personal":
				if p := r.Providers[hop.Provider]; len(p.BrokerGrants) == 0 {
					return fmt.Errorf("models.yaml: model %q provider %q personal sharing needs broker_grants", name, hop.Provider)
				}
			default:
				return fmt.Errorf("models.yaml: model %q sharing %q unknown", name, hop.Sharing)
			}
			for _, limit := range hop.Limits {
				if limit.Amount <= 0 {
					return fmt.Errorf("models.yaml: model %q limit needs amount > 0", name)
				}
				switch limit.Of {
				case "requests", "tokens", "provider_quota", "spend_usd":
				default:
					return fmt.Errorf("models.yaml: model %q limit of %q unknown", name, limit.Of)
				}
				if limit.Unit != "" && limit.Unit != "percent" {
					return fmt.Errorf("models.yaml: model %q limit unit %q unknown", name, limit.Unit)
				}
				if limit.Clock != "" && limit.Clock != "provider" {
					return fmt.Errorf("models.yaml: model %q limit clock %q unknown", name, limit.Clock)
				}
				switch limit.Per {
				case "5h", "1d", "1w":
				default:
					return fmt.Errorf("models.yaml: model %q limit per %q unknown", name, limit.Per)
				}
				if limit.Scope != "" && limit.Scope != "shared" && limit.Scope != "personal" {
					return fmt.Errorf("models.yaml: model %q limit scope %q unknown", name, limit.Scope)
				}
			}
		}
	}
	for owner, level := range r.Switch {
		if owner != "*" && !idPattern.MatchString(owner) {
			return fmt.Errorf("models.yaml: switch has invalid owner %q", owner)
		}
		if level != "none" && level != "logical" && level != "catalog" {
			return fmt.Errorf("models.yaml: switch level %q unknown", level)
		}
	}
	return nil
}
