package stack

import (
	"errors"
	"fmt"
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
	Providers map[string]ModelProvider `yaml:"providers"`
	Models    map[string]ModelEntry    `yaml:"models"`
}

type ModelProvider struct {
	URL    string `yaml:"url"`
	KeyEnv string `yaml:"key_env,omitempty"`
}

type ModelHopRef struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
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
	}
	return reg, reg.validate()
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

func (r *ModelRegistry) validate() error {
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
	}
	for name, m := range r.Models {
		if !registryNamePattern.MatchString(name) {
			return fmt.Errorf("models.yaml: invalid model name %q", name)
		}
		if len(m.Ladder) == 0 {
			return fmt.Errorf("models.yaml: model %q needs a ladder", name)
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
			if strings.TrimSpace(hop.Model) == "" || !registryModelPattern.MatchString(hop.Model) {
				return fmt.Errorf("models.yaml: model %q bad provider model id %q", name, hop.Model)
			}
		}
	}
	return nil
}
