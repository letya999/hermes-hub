package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// profilesFile is spaces/profiles.yaml: named overlays of the model/provider
// settings operators share between spaces. Layers resolve deterministically —
// the "default" profile first, then every named profile listing the owner —
// and a profile only fills fields the space itself left unset, so the owner's
// own file is always the final say ("default -> group -> owner override").
type profilesFile struct {
	Profiles map[string]profileOverlay `yaml:"profiles"`
}

// profileOverlay carries only scalar/small settings — never secret literals.
// model_fallbacks entries reference secrets by env name via key_env.
type profileOverlay struct {
	Owners   []string `yaml:"owners,omitempty"`
	Model    string   `yaml:"model,omitempty"`
	ModelURL string   `yaml:"model_url,omitempty"`
	Cliproxy string   `yaml:"cliproxy,omitempty"`
}

// applyProfiles loads spaces/profiles.yaml relative to the space dir and
// fills zero-valued fields. Outside the spaces/ layout it is a no-op.
func applyProfiles(s *Settings, dir string) error {
	root := filepath.Dir(dir)
	if filepath.Base(root) != "spaces" {
		return nil
	}
	path := filepath.Join(root, "profiles.yaml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	var pf profilesFile
	if err := d.Decode(&pf); err != nil {
		return fmt.Errorf("profiles.yaml: %w", err)
	}
	names := make([]string, 0, len(pf.Profiles))
	for name := range pf.Profiles {
		if !idPattern.MatchString(name) && name != "default" {
			return fmt.Errorf("profiles.yaml: invalid profile name %q", name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	// Fields the owner's own file set are final; profile layers apply
	// default -> named (sorted) and each later layer wins over earlier ones.
	ownModel, ownCliproxy := s.Model != "", s.Cliproxy != ""
	ownModelURL := s.ModelURL != ""
	ordered := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "default" })
	ordered = append([]string{"default"}, ordered...)
	for _, name := range ordered {
		o, ok := pf.Profiles[name]
		if !ok || (name != "default" && !slices.Contains(o.Owners, s.User)) {
			continue
		}
		if !ownModel && o.Model != "" {
			s.Model = o.Model
		}
		if !ownCliproxy && o.Cliproxy != "" {
			s.Cliproxy = o.Cliproxy
		}
		if !ownModelURL && o.ModelURL != "" {
			s.ModelURL = o.ModelURL
		}
	}
	return nil
}
