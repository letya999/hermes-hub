package skillstore

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the normalized SKILL.md frontmatter view (D2 compat fields
// folded into Requires/Settings).
type Manifest struct {
	Name                   string
	Description            string
	License                string
	AllowedTools           []string
	Platforms              []string
	Requires               Requires
	DisableModelInvocation bool
	UserInvocable          *bool
	ArgumentHint           string
	AllowImplicitInvocation *bool
	Settings               []SettingSpec
	Metadata               map[string]any
}

// Requires aggregates cross-format capability/env/bin requirements.
type Requires struct {
	Toolsets             []string
	Tools                []string
	FallbackForToolsets  []string
	FallbackForTools     []string
	Env                  []EnvRequirement
	Bins                 []string
	Connectors           []string
	Deps                 []string
}

// EnvRequirement is a named credential/env need — names only, never values.
type EnvRequirement struct {
	Name        string `json:"name"`
	Prompt      string `json:"prompt,omitempty"`
	Help        string `json:"help,omitempty"`
	RequiredFor string `json:"required_for,omitempty"`
}

// SettingSpec is a non-secret per-user skill setting.
type SettingSpec struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

// ParseSkillMD parses SKILL.md frontmatter. lenient=true tolerates
// foreign-format deviations and reports them as warnings; lenient=false
// enforces the hub-authored contract (name == dir, description present).
func ParseSkillMD(dir string, body []byte, lenient bool) (*Manifest, []string, error) {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil, fmt.Errorf("%w: SKILL.md must start with a YAML frontmatter block", ErrInvalid)
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, nil, fmt.Errorf("%w: unterminated frontmatter", ErrInvalid)
	}
	var fm struct {
		Name                   string   `yaml:"name"`
		Description            string   `yaml:"description"`
		Platforms              []string `yaml:"platforms"`
		DisableModelInvocation bool     `yaml:"disable-model-invocation"`
		UserInvocable          *bool    `yaml:"user-invocable"`
		ArgumentHint           string   `yaml:"argument-hint"`
		AllowedTools           []string `yaml:"allowed-tools"`
		AllowedToolsAlt        []string `yaml:"allowed_tools"`
		RequiredEnv            []EnvRequirement `yaml:"required_environment_variables"`
		Metadata               map[string]any   `yaml:"metadata"`
	}
	_ = yaml.Unmarshal([]byte(rest[:end]), &fm)
	m := &Manifest{
		Name: fm.Name, Description: fm.Description,
		Platforms:              fm.Platforms,
		DisableModelInvocation: fm.DisableModelInvocation,
		UserInvocable:          fm.UserInvocable,
		ArgumentHint:           fm.ArgumentHint,
		AllowedTools:           append(append([]string{}, fm.AllowedTools...), fm.AllowedToolsAlt...),
		Metadata:               fm.Metadata,
	}
	m.Requires.Env = append(m.Requires.Env, fm.RequiredEnv...)
	foldCompat(m)
	warns := []string{}
	if m.Name != dir {
		if !lenient {
			return nil, nil, fmt.Errorf("%w: frontmatter name %q must equal directory %q", ErrInvalid, m.Name, dir)
		}
		warns = append(warns, fmt.Sprintf("frontmatter name %q differs from directory %q", m.Name, dir))
	}
	if strings.TrimSpace(m.Description) == "" && !lenient {
		return nil, nil, fmt.Errorf("%w: description is required", ErrInvalid)
	}
	if len(m.Description) > 1024 {
		return nil, nil, fmt.Errorf("%w: description exceeds 1024 chars", ErrInvalid)
	}
	return m, warns, nil
}

// foldCompat normalizes foreign frontmatter shapes into Requires (D2):
// metadata.hermes.{requires_*,fallback_for_toolsets} and
// metadata.openclaw.requires.{bins,env}.
func foldCompat(m *Manifest) {
	if m.Metadata == nil {
		return
	}
	strs := func(v any) []string {
		list, _ := v.([]any)
		out := []string{}
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if h, ok := m.Metadata["hermes"].(map[string]any); ok {
		m.Requires.Toolsets = append(m.Requires.Toolsets, strs(h["requires_toolsets"])...)
		m.Requires.Tools = append(m.Requires.Tools, strs(h["requires_tools"])...)
		m.Requires.FallbackForToolsets = append(m.Requires.FallbackForToolsets, strs(h["fallback_for_toolsets"])...)
	}
	if oc, ok := m.Metadata["openclaw"].(map[string]any); ok {
		if req, ok := oc["requires"].(map[string]any); ok {
			m.Requires.Bins = append(m.Requires.Bins, strs(req["bins"])...)
			for _, env := range strs(req["env"]) {
				m.Requires.Env = append(m.Requires.Env, EnvRequirement{Name: env})
			}
		}
	}
}
