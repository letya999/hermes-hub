package stack

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
)

// ModelDoctor reports effective provider scope and owner-local apply actions.
func ModelDoctor(dir string, s Settings, secrets map[string]string) ([]string, error) {
	registry, err := LoadModelRegistry(dir, s)
	if err != nil {
		return nil, err
	}
	if len(registry.Models) == 0 {
		registry.Models["default"] = ModelEntry{Ladder: []ModelHopRef{{Provider: "cliproxy", Model: s.Model}}}
	}
	registry.RuntimeModel = s.Model
	var lines []string
	keys := map[string]bool{}
	names := make([]string, 0, len(registry.Providers))
	for name := range registry.Providers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		provider := registry.Providers[name]
		scope := "anonymous"
		if provider.KeyEnv != "" {
			scope = "owner-env"
			if provider.Allows(s.User) {
				keys[provider.KeyEnv] = true
			}
		}
		if provider.BrokerGrants != nil {
			scope = "owner-broker"
		}
		lines = append(lines, fmt.Sprintf("Model provider=%s endpoint=%s credential_scope=%s owner=%s authorized=%t", name, provider.URL, scope, s.User, provider.Allows(s.User)))
	}
	for _, model := range registry.Models {
		for _, hop := range model.Ladder {
			if hop.Provider == "cliproxy" && registry.Providers["cliproxy"].KeyEnv == "" && len(registry.Providers["cliproxy"].BrokerGrants) == 0 {
				keys["OPENAI_API_KEY"] = true
			}
		}
	}
	desired, err := yaml.Marshal(registry)
	if err != nil {
		return nil, err
	}
	rendered, err := os.ReadFile(filepath.Join(dir, "generated", "model-registry", s.Environment, "models.yaml"))
	if err != nil {
		lines = append(lines, "Model registry not rendered; render this owner before deployment")
	} else {
		var old ModelRegistry
		if yaml.Unmarshal(rendered, &old) != nil {
			return nil, errors.New("rendered model registry invalid")
		}
		if bytes.Equal(desired, rendered) {
			lines = append(lines, "Model registry unchanged")
		} else {
			lines = append(lines, "Model apply: render registry; relay reloads without restart")
		}
		if old.RuntimeModel == s.Model {
			lines = append(lines, "Model runtime unchanged")
		} else {
			lines = append(lines, "Model apply: next owner runtime spawn uses the new model")
		}
	}
	environment, err := ReadSecrets(filepath.Join(dir, "runtime."+s.Environment+".env"))
	if err != nil {
		lines = append(lines, "Model provider env not rendered; render before comparing credentials")
	} else {
		changed := false
		for key := range keys {
			changed = changed || environment[key] != secrets[key]
		}
		if changed {
			lines = append(lines, "Model credential apply: render and recreate owner model-relay")
		} else {
			lines = append(lines, "Model credentials unchanged; Broker credentials resolve per call")
		}
	}
	compose, err := os.ReadFile(filepath.Join(dir, "generated", "compose."+s.Environment+".yaml"))
	if err != nil {
		lines = append(lines, "Model proxy config not rendered; render before comparing placement")
	} else {
		var old struct {
			Services map[string]struct {
				Environment map[string]string `yaml:"environment"`
			} `yaml:"services"`
		}
		if yaml.Unmarshal(compose, &old) != nil {
			return nil, errors.New("rendered model compose invalid")
		}
		desiredProxy := "http://cliproxy:8317"
		if s.Cliproxy == "dedicated" {
			desiredProxy = "http://cliproxy-" + s.User + ":8317"
		}
		if old.Services["model-relay"].Environment["HUB_MODEL_RELAY_CLIPROXY"] == desiredProxy {
			lines = append(lines, "Model proxy unchanged")
		} else {
			lines = append(lines, "Model placement apply: render and recreate owner proxy and model-relay")
		}
	}
	return lines, nil
}
