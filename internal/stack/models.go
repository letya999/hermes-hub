package stack

import (
	"errors"
	"fmt"
	brokeridentity "github.com/letya999/credential-broker/identity"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelRegistry is the merged view of the model/provider layer declared in
// models.yaml files: shared under spaces/, then the organization space, then
// the owner's own space — later layers win on the same keys.
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
// a fixed {of, amount, per} grammar. "requests" counts every upstream call,
// "tokens" reserves the conservative request estimate up front. Counters live
// in the relay state directory and never cross spaces; scope "shared" meters
// the principal, "personal" meters each owner separately.
type ModelLimit struct {
	Of     string `yaml:"of"`
	Amount int64  `yaml:"amount"`
	Per    string `yaml:"per"`
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

// LoadModelRegistry merges spaces/models.yaml, spaces/<org>/models.yaml and
// spaces/<user>/models.yaml for the space rooted at dir.
func LoadModelRegistry(dir string, s Settings) (*ModelRegistry, error) {
	reg := &ModelRegistry{Providers: map[string]ModelProvider{}, Models: map[string]ModelEntry{}}
	root := filepath.Dir(filepath.Clean(dir))
	if filepath.Base(root) != "spaces" {
		return reg, nil
	}
	paths := []string{filepath.Join(root, "models.yaml")}
	if s.Organization != "" {
		paths = append(paths, filepath.Join(root, s.Organization, "models.yaml"))
	}
	paths = append(paths, filepath.Join(dir, "models.yaml"))
	for _, path := range paths {
		layer, err := readModelRegistry(path)
		if err != nil {
			return nil, err
		}
		for name, p := range layer.Providers {
			reg.Providers[name] = p
		}
		for name, m := range layer.Models {
			reg.Models[name] = m
		}
		if reg.Switch == nil {
			reg.Switch = map[string]string{}
		}
		for name, level := range layer.Switch {
			reg.Switch[name] = level
		}
	}
	// The published models doc is the imperative runtime contract: routes and
	// the switch policy come from it alone. Providers it declares take the
	// endpoint/credential the passport carries; broker grant ids and owner
	// scoping stay a credential-plane detail and pass through from the legacy
	// layer under the same provider name.
	if layer := passportRegistryLayer(dir); len(layer.Models)+len(layer.Providers)+len(layer.Switch) > 0 {
		for name, p := range layer.Providers {
			if old, ok := reg.Providers[name]; ok {
				p.BrokerGrants = old.BrokerGrants
				p.Owners = old.Owners
				p.KeyEnv = old.KeyEnv
				p.AllowPrivate = old.AllowPrivate
			}
			reg.Providers[name] = p
		}
		reg.Models = layer.Models
		if reg.Switch == nil {
			reg.Switch = map[string]string{}
		}
		for name, level := range layer.Switch {
			reg.Switch[name] = level
		}
	}
	return reg, reg.Validate()
}

func readModelRegistry(path string) (modelRegistryYAML, error) {
	var layer modelRegistryYAML
	b, err := os.ReadFile(path) // #nosec G304 -- operator-controlled registry path
	if os.IsNotExist(err) {
		return layer, nil
	} else if err != nil {
		return layer, err
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err := d.Decode(&layer); err != nil {
		return layer, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return layer, nil
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
				case "requests", "tokens":
				default:
					// provider_quota_percent needs verified session telemetry
					// and spend needs a price feed — neither exists yet, so the
					// configuration fails closed instead of drifting unbounded.
					return fmt.Errorf("models.yaml: model %q limit of %q requires verified usage telemetry", name, limit.Of)
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
