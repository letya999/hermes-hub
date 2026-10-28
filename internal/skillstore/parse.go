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
	Credentials         []EnvRequirement `json:"credentials,omitempty"`
	Bins                []string
	Connectors          []string
	Deps                []string
}

// EnvRequirement is a named credential/env need — names only, never values.
// Hard marks the credential a hard dependency: a missing value hides the
// skill instead of merely reporting "setup needed" (R8.5).
type EnvRequirement struct {
	Name        string `json:"name"`
	Prompt      string `json:"prompt,omitempty"`
	Help        string `json:"help,omitempty"`
	RequiredFor string `json:"required_for,omitempty"`
	Hard        bool   `json:"hard,omitempty"`
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
			Toolsets    []string `yaml:"toolsets"`
			Connectors  []string `yaml:"connectors"`
			Credentials []any    `yaml:"credentials"`
			Env         []any    `yaml:"env"`
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
	m.Requires.Credentials = append(m.Requires.Credentials, fm.RequiredEnv...)
	m.Requires.Credentials = append(m.Requires.Credentials, credentialItems(fm.Requires.Credentials)...)
	m.Requires.Credentials = append(m.Requires.Credentials, credentialItems(fm.Requires.Env)...)
	foldCompat(m)
	for _, cred := range m.Requires.Credentials {
		if ReservedSecretName(cred.Name) {
			return nil, nil, fmt.Errorf("%w: credential name %q is hub/broker-reserved", ErrInvalid, cred.Name)
		}
	}
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
		for _, env := range strs(h["requires_env"]) {
			m.Requires.Credentials = append(m.Requires.Credentials, EnvRequirement{Name: env})
		}
		if config, ok := h["config"].(map[string]any); ok {
			for key, spec := range config {
				setting := SettingSpec{Key: key}
				switch v := spec.(type) {
				case string:
					setting.Default = v
				case map[string]any:
					setting.Description, _ = v["description"].(string)
					setting.Default, _ = v["default"].(string)
					setting.Prompt, _ = v["prompt"].(string)
				}
				m.Settings = append(m.Settings, setting)
			}
		}
	}
	if oc, ok := m.Metadata["openclaw"].(map[string]any); ok {
		if req, ok := oc["requires"].(map[string]any); ok {
			m.Requires.Bins = append(m.Requires.Bins, strs(req["bins"])...)
			for _, env := range strs(req["env"]) {
				m.Requires.Credentials = append(m.Requires.Credentials, EnvRequirement{Name: env})
			}
		}
	}
	for _, dep := range strs(m.Metadata["deps"]) {
		if norm := normalizeDep(dep); norm != "" {
			m.Requires.Deps = append(m.Requires.Deps, norm)
		}
	}
}

// ReservedSecretName reports whether an env name belongs to the hub or the
// credential broker (R8.1) — a skill declaring one would try to lease
// hub-owned infrastructure values into cells.
func ReservedSecretName(name string) bool {
	return strings.HasPrefix(name, "HUB_") || strings.HasPrefix(name, "BROKER_")
}

// credentialItems accepts the two credential declaration shapes — a bare
// name string or a {name,prompt,hard} object (R8.1).
func credentialItems(items []any) []EnvRequirement {
	out := []EnvRequirement{}
	for _, item := range items {
		switch v := item.(type) {
		case string:
			out = append(out, EnvRequirement{Name: v})
		case map[string]any:
			name, _ := v["name"].(string)
			prompt, _ := v["prompt"].(string)
			out = append(out, EnvRequirement{Name: name, Prompt: prompt, Hard: v["hard"] == true})
		}
	}
	return out
}

// codexOpenAI folds Codex agents/openai.yaml into the manifest (D2/R8.1):
// dependencies.tools widen requires.tools and
// policy.allow_implicit_invocation gates the catalog's implicit surface.
func codexOpenAI(tree Tree, m *Manifest) {
	f, ok := tree["agents/openai.yaml"]
	if !ok {
		return
	}
	var cfg struct {
		Dependencies struct {
			Tools []string `yaml:"tools"`
		} `yaml:"dependencies"`
		Policy struct {
			AllowImplicit *bool `yaml:"allow_implicit_invocation"`
		} `yaml:"policy"`
	}
	if yaml.Unmarshal(f.Data, &cfg) != nil {
		return
	}
	m.Requires.Tools = append(m.Requires.Tools, cfg.Dependencies.Tools...)
	if cfg.Policy.AllowImplicit != nil {
		m.AllowImplicitInvocation = cfg.Policy.AllowImplicit
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
