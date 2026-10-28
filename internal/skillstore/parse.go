package skillstore

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the normalized SKILL.md frontmatter view (D2 compat fields
// folded into Requires/Settings).
type Manifest struct {
	Name                    string
	Description             string
	License                 string
	AllowedTools            []string
	Tags                    []string
	Platforms               []string
	Requires                Requires
	DisableModelInvocation  bool
	UserInvocable           *bool
	ArgumentHint            string
	AllowImplicitInvocation *bool
	Settings                []SettingSpec
	Metadata                map[string]any
}

// Requires aggregates cross-format capability/env/bin requirements.
type Requires struct {
	Toolsets            []string
	Tools               []string
	FallbackForToolsets []string
	FallbackForTools    []string
	Env                 []EnvRequirement
	Bins                []string
	Connectors          []string
	Deps                []string
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
		Name                   string           `yaml:"name"`
		Description            string           `yaml:"description"`
		Tags                   []string         `yaml:"tags"`
		Platforms              []string         `yaml:"platforms"`
		DisableModelInvocation bool             `yaml:"disable-model-invocation"`
		UserInvocable          *bool            `yaml:"user-invocable"`
		ArgumentHint           string           `yaml:"argument-hint"`
		AllowedTools           []string         `yaml:"allowed-tools"`
		AllowedToolsAlt        []string         `yaml:"allowed_tools"`
		RequiredEnv            []EnvRequirement `yaml:"required_environment_variables"`
		AllowImplicit          *bool            `yaml:"allow_implicit_invocation"`
		Requires               struct {
			Toolsets   []string `yaml:"toolsets"`
			Connectors []string `yaml:"connectors"`
		} `yaml:"requires"`
		Metadata map[string]any `yaml:"metadata"`
	}
	_ = yaml.Unmarshal([]byte(rest[:end]), &fm)
	m := &Manifest{
		Name: fm.Name, Description: fm.Description,
		Tags:                    fm.Tags,
		Platforms:               fm.Platforms,
		DisableModelInvocation:  fm.DisableModelInvocation,
		UserInvocable:           fm.UserInvocable,
		ArgumentHint:            fm.ArgumentHint,
		AllowedTools:            append(append([]string{}, fm.AllowedTools...), fm.AllowedToolsAlt...),
		AllowImplicitInvocation: fm.AllowImplicit,
		Metadata:                fm.Metadata,
	}
	m.Requires.Toolsets = append(m.Requires.Toolsets, fm.Requires.Toolsets...)
	m.Requires.Connectors = append(m.Requires.Connectors, fm.Requires.Connectors...)
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
	for _, dep := range strs(m.Metadata["deps"]) {
		if norm := normalizeDep(dep); norm != "" {
			m.Requires.Deps = append(m.Requires.Deps, norm)
		}
	}
}

// depPattern is the R7.4 dependency allowlist shape: a pip:/npm:/system:
// scheme, a package-name charset, and an optional version constraint that
// admits no spaces, URLs or shell syntax — anything else is dropped.
var depPattern = regexp.MustCompile(`^(pip|npm|system):[A-Za-z0-9][A-Za-z0-9._-]*([<>=!~@][A-Za-z0-9.*+<>=,!~|-]*)?$`)

// runnerPinRE recognizes exactly one pinned runner invocation —
// `uv run --with <pkg>@<ver>` or `npx -y <pkg>@<ver>` — and captures the
// pinned spec. Unpinned runners, extra flags and shell-shaped package
// strings never match (R7.4).
var (
	uvPinRE  = regexp.MustCompile(`^uv run --with ([A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9.*+<>=,!~|-]+)$`)
	npxPinRE = regexp.MustCompile(`^npx -y ([A-Za-z0-9][A-Za-z0-9._-]*@[A-Za-z0-9.*+<>=,!~|-]+)$`)
)

// normalizeDep maps a declaration to its allowlisted dep spec: runner pins
// fold into the pip:/npm: schemes, already-scheme-shaped entries pass
// through unchanged, everything else returns "" and is dropped.
func normalizeDep(dep string) string {
	if m := uvPinRE.FindStringSubmatch(dep); m != nil {
		return "pip:" + m[1]
	}
	if m := npxPinRE.FindStringSubmatch(dep); m != nil {
		return "npm:" + m[1]
	}
	if depPattern.MatchString(dep) {
		return dep
	}
	return ""
}

func validDep(dep string) bool { return depPattern.MatchString(dep) }

// pep723Deps extracts PEP 723 `dependencies` items from scripts/*.py files
// (R7.4). Entries are bare package specs — validated after the pip: prefix;
// injection-shaped lines drop silently like metadata.deps entries.
func pep723Deps(tree Tree) []string {
	deps := []string{}
	for path, f := range tree {
		if !strings.HasPrefix(path, "scripts/") || !strings.HasSuffix(path, ".py") {
			continue
		}
		inBlock, inList := false, false
		for _, line := range strings.Split(string(f.Data), "\n") {
			trimmed := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			switch {
			case !inBlock && strings.HasPrefix(trimmed, "/// script"):
				inBlock = true
			case inBlock && strings.HasPrefix(trimmed, "///"):
				inBlock = false
			case inBlock && strings.HasPrefix(trimmed, "dependencies"):
				inList = true
			case inBlock && inList:
				if strings.Contains(trimmed, "]") {
					inList = false
					continue
				}
				name := strings.Trim(strings.TrimSpace(strings.TrimSuffix(trimmed, ",")), `"'`)
				if dep := "pip:" + name; validDep(dep) {
					deps = append(deps, dep)
				}
			}
		}
	}
	return deps
}
