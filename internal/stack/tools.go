package stack

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultSettingsYAML is the template every space starts from — the single
// defaults file an operator edits once for all users (ax-style seed).
//
//go:embed defaults/settings.yaml
var defaultSettingsYAML []byte

// renderDefaultSettings stamps the template with the space identity while
// preserving its comments.
func renderDefaultSettings(user, organization string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(defaultSettingsYAML, &doc); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	orgSet := organization == ""
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "user":
			root.Content[i+1].Value = user
		case "organization":
			root.Content[i+1].Value = organization
			orgSet = true
		}
	}
	if !orgSet {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "organization"}, &yaml.Node{Kind: yaml.ScalarNode, Value: organization})
	}
	return yaml.Marshal(&doc)
}

// ToolEntry is one line of the settings `tools:` map — the single operator
// surface for "how is this capability served". The scalar form is just the
// backend (`terminal: native`); the long form adds per-tool toggles, ToolHub
// scopes and limits:
//
//	tools:
//	  terminal: native            # upstream toolset inside the runtime
//	  file: toolhub               # managed executor, per-call admission
//	  browser: mcp:playwright     # an MCP server (builtin catalog or mcp_servers)
//	  github: mcp                 # connector capability (via ToolHub when managed)
//	  code_exec: off              # explicit deny
//	  ssh:
//	    via: toolhub
//	    tools: {write: true, shell: false}
//	  file:
//	    via: toolhub
//	    paths: [docs, inbox]
//	    limits: {output_bytes: 65536}
type ToolEntry struct {
	Via    string          `yaml:"via,omitempty"`
	Server string          `yaml:"server,omitempty"`
	Only   []string        `yaml:"only,omitempty"`
	Except []string        `yaml:"except,omitempty"`
	Tools  map[string]bool `yaml:"tools,omitempty"`
	Paths  []string        `yaml:"paths,omitempty"`
	Limits map[string]int  `yaml:"limits,omitempty"`
}

// Tool backends. `off` (or absence) is the default-deny state.
const (
	ToolViaNative  = "native"
	ToolViaToolHub = "toolhub"
	ToolViaMCP     = "mcp"
	ToolViaOff     = "off"
)

// UnmarshalYAML accepts either the scalar backend form (`terminal: native`,
// `browser: mcp:playwright`) or the long mapping form.
func (e *ToolEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		via, server := value.Value, ""
		if strings.HasPrefix(via, "mcp:") {
			via, server = "mcp", strings.TrimPrefix(via, "mcp:")
		}
		e.Via, e.Server = via, server
		return nil
	}
	type plain ToolEntry
	return value.Decode((*plain)(e))
}

func (e ToolEntry) enabled() bool { return e.Via != "" && e.Via != ToolViaOff }

// toolhubFamilies are the capability families the managed agenttools executor
// can serve. internal/agenttools tests assert its catalog stays within this
// set — the names are the operator-facing vocabulary, not implementation.
var toolhubFamilies = map[string]bool{
	"files": true, "file": true, "documents": true, "docs": true,
	"images": true, "artifacts": true, "routines": true, "services": true,
	"hh": true, "ssh": true, "image_gen": true, "terminal": true,
	"code_exec": true, "web": true,
}

// mcpConnectors are builtin MCP-provided capabilities: rendered directly only
// in unmanaged mode where a local server exists; under managed mode they are
// projected through ToolHub and the entry is intent plus feature gating.
var mcpConnectors = map[string]bool{
	"browser": true, "github": true, "gitlab": true, "google": true,
	"slack": true, "atlassian": true, "telegram_user": true,
	"desktop": true, "drafts": true,
}

// pseudoToolsets are `via: native` entries that are not upstream toolsets but
// native subsystems: meet maps to the google_meet plugin toolset (unmanaged
// only — plugins stay sealed under managed), transcription is the stt flag.
var pseudoToolsets = map[string]bool{"meet": true, "transcription": true}

// ingressChannels are transport adapters that feed messages into the agent.
// They are operator-owned ingress, never agent tools.
var ingressChannels = map[string]bool{"telegram": true, "slack_app": true}

// toolToggleTokens are named sub-capability switches a `tools:` sub-map may
// flip per family — bundles the feature vocabulary used to expose.
var toolToggleTokens = map[string]map[string]bool{
	"browser":       {"act": true},
	"ssh":           {"write": true, "shell": true, "tunnel": true},
	"google":        {"write": true},
	"telegram_user": {"write": true},
}

// Workspace declares what enters the runtime container beyond the fixed
// layout. `from` is a path relative to the space directory, or `org:<path>`
// for an organization-owned directory (always read-only); `to` is an
// absolute container path outside the reserved roots.
type Workspace struct {
	Mounts []Mount `yaml:"mounts,omitempty"`
}

type Mount struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	Mode string `yaml:"mode,omitempty"` // rw (default) | ro
}

var reservedMountRoots = []string{"/state", "/config", "/workspace", "/archive", "/org", "/opt/hub", "/scratch", "/etc", "/proc", "/sys", "/dev"}

func (w Workspace) validate() error {
	seen := map[string]bool{}
	for _, m := range w.Mounts {
		from := strings.TrimPrefix(m.From, "org:")
		if from == "" || strings.HasPrefix(from, "/") || strings.Contains(from, "..") || strings.ContainsAny(from, "\\\x00") {
			return fmt.Errorf("invalid workspace mount source %q", m.From)
		}
		if !strings.HasPrefix(m.To, "/") || strings.Contains(m.To, "..") {
			return fmt.Errorf("invalid workspace mount target %q", m.To)
		}
		for _, root := range reservedMountRoots {
			if m.To == root || strings.HasPrefix(m.To, root+"/") {
				return fmt.Errorf("workspace mount target %q collides with a reserved root", m.To)
			}
		}
		if m.Mode != "" && m.Mode != "ro" && m.Mode != "rw" {
			return fmt.Errorf("workspace mount mode must be ro or rw")
		}
		if strings.HasPrefix(m.From, "org:") && m.Mode != "" && m.Mode != "ro" {
			return fmt.Errorf("organization mounts are always read-only")
		}
		if seen[m.To] {
			return fmt.Errorf("duplicate workspace mount target %q", m.To)
		}
		seen[m.To] = true
	}
	return nil
}

// resolveMount maps a declared mount to its host source. Space-relative
// sources stay inside SpaceDir; `org:` sources resolve under
// OrganizationDir and are always read-only.
func (s Settings) resolveMount(m Mount) (source string, readOnly bool, err error) {
	rel := m.From
	readOnly = m.Mode == "ro"
	if strings.HasPrefix(rel, "org:") {
		if s.OrganizationDir == "" {
			return "", false, fmt.Errorf("mount %q needs an organization dir", m.From)
		}
		rel = strings.TrimPrefix(rel, "org:")
		return filepath.Join(s.OrganizationDir, filepath.FromSlash(rel)), true, nil
	}
	return filepath.Join(s.SpaceDir, filepath.FromSlash(rel)), readOnly, nil
}

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var toolTogglePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var toolPathPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,255}$`)
var toolLimitPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (s Settings) toolEntry(name string) ToolEntry {
	if s.Tools == nil {
		return ToolEntry{}
	}
	return s.Tools[name]
}

// nativeCarveouts returns the sorted reviewed upstream toolsets the operator
// re-enabled for this runtime — the derivation consumed by the managed
// denylist, materialization options and the runtime attestation env.
func (s Settings) nativeCarveouts() []string {
	names := []string{}
	for name, entry := range s.Tools {
		if entry.Via == ToolViaNative && nativeCarveoutToolsets[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// NativeCarveouts exposes the derived list for callers outside the package
// (supervisor env contract, capability CLI).
func (s Settings) NativeCarveouts() []string { return s.nativeCarveouts() }

// nativeToolsEntries rebuilds the tools-map form of a plain carve-out list —
// used where the attestation path receives names rather than settings.
func nativeToolsEntries(names []string) map[string]ToolEntry {
	entries := make(map[string]ToolEntry, len(names))
	for _, name := range names {
		entries[name] = ToolEntry{Via: ToolViaNative}
	}
	return entries
}

// LegacyFeatureTools converts schema-1 feature names into their `tools:`/
// `ingress:` equivalents. Read uses it for migration; tests use it to write
// fixtures in the old vocabulary without keeping the field alive.
func LegacyFeatureTools(features []string) (map[string]ToolEntry, []string, error) {
	tools := map[string]ToolEntry{}
	var ingress []string
	for _, feature := range features {
		switch {
		case feature == "workspace":
		case feature == "telegram" || feature == "slack_app":
			ingress = append(ingress, feature)
		case featureToolMigration[feature].Via != "":
			tools[feature] = featureToolMigration[feature]
		default:
			pair, ok := featureToggleMigration[feature]
			if !ok {
				return nil, nil, fmt.Errorf("unknown legacy feature %q", feature)
			}
			e := tools[pair[0]]
			if e.Via == "" {
				e = featureToolMigration[pair[0]]
			}
			if e.Tools == nil {
				e.Tools = map[string]bool{}
			}
			e.Tools[pair[1]] = true
			tools[pair[0]] = e
		}
	}
	return tools, ingress, nil
}

// ToolPlan is the compiled view of the `tools:` map — what each backend
// actually serves for this space. `hubctl capability --kind tools` prints it.
type ToolPlan struct {
	Native  []string          `json:"native"`
	ToolHub []string          `json:"toolhub"`
	MCP     map[string]string `json:"mcp"`
	Off     []string          `json:"off"`
	Ingress []string          `json:"ingress"`
	Mounts  []Mount           `json:"mounts"`
}

// ToolPlan compiles the tools map into per-backend groupings. MCP values name
// the resolved server (entry.Server, defaulting to the entry name).
func (s Settings) ToolPlan() ToolPlan {
	plan := ToolPlan{MCP: map[string]string{}, Mounts: s.Workspace.Mounts, Ingress: append([]string(nil), s.Ingress...)}
	for name, entry := range s.Tools {
		switch entry.Via {
		case ToolViaNative:
			plan.Native = append(plan.Native, name)
		case ToolViaToolHub:
			plan.ToolHub = append(plan.ToolHub, name)
		case ToolViaMCP:
			server := entry.Server
			if server == "" {
				server = name
			}
			plan.MCP[name] = server
		case ToolViaOff:
			plan.Off = append(plan.Off, name)
		}
	}
	slices.Sort(plan.Native)
	slices.Sort(plan.ToolHub)
	slices.Sort(plan.Off)
	return plan
}

// disabledMCPNames preserves the org-narrowing semantics the removed
// `disabled_mcp` field carried: a tools entry explicitly set to `off`.
func (s Settings) disabledMCPNames() []string {
	names := []string{}
	for name, entry := range s.Tools {
		if entry.Via == ToolViaOff {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// DisabledMCPNames reports explicitly denied tool entries for the org-scope
// and migration paths that used to read `disabled_mcp`.
func (s Settings) DisabledMCPNames() []string { return s.disabledMCPNames() }

// toolToggleFeatureNames maps name+token pairs back to the classic feature
// vocabulary so HUB_FEATURES, org ceilings and Doctor keep the same words.
var toolToggleFeatureNames = map[string]map[string]string{
	"browser":       {"act": "browser_act"},
	"ssh":           {"write": "ssh_write", "shell": "ssh_shell", "tunnel": "ssh_tunnel"},
	"google":        {"write": "google_write"},
	"telegram_user": {"write": "telegram_write"},
}

// featureList derives the classic feature names runtime-side consumers read
// from HUB_FEATURES (ServiceCatalog checks, communication config, services)
// and the vocabulary organization ceilings govern. Tool entries that are not
// classic features — the ToolHub families and upstream toolsets — never
// appear: they are admitted by profile/grant, not by feature policy.
func (s Settings) featureList() []string {
	out := []string{"workspace"}
	for name, entry := range s.Tools {
		if !entry.enabled() {
			continue
		}
		if slices.ContainsFunc(Features, func(f Feature) bool { return f.Name == name }) {
			out = append(out, name)
		}
		for token, on := range entry.Tools {
			if !on {
				continue
			}
			if mapped, ok := toolToggleFeatureNames[name][token]; ok {
				out = append(out, mapped)
			}
		}
	}
	out = append(out, s.Ingress...)
	slices.Sort(out)
	return out
}

func (s Settings) toolToggle(name, token string) bool {
	e := s.toolEntry(name)
	return e.enabled() && e.Tools[token]
}

// Has reports feature enablement in the pre-`tools:` vocabulary so every
// render, doctor and scope call site keeps one lookup. Derived, never stored.
func (s Settings) Has(name string) bool {
	switch name {
	case "workspace":
		return true
	case "telegram", "slack_app":
		return slices.Contains(s.Ingress, name)
	case "browser_act":
		return s.toolToggle("browser", "act")
	case "ssh_write":
		return s.toolToggle("ssh", "write")
	case "ssh_shell":
		return s.toolToggle("ssh", "shell")
	case "ssh_tunnel":
		return s.toolToggle("ssh", "tunnel")
	case "google_write":
		return s.toolToggle("google", "write")
	case "telegram_write":
		return s.toolToggle("telegram_user", "write")
	default:
		return s.toolEntry(name).enabled()
	}
}

// validateTools checks the whole `tools:` map: names, backend membership,
// per-backend field discipline and the managed/native boundary.
func (s Settings) validateTools() error {
	managed := s.CapabilityMode == "managed"
	for name, e := range s.Tools {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("invalid tools entry name %q", name)
		}
		switch e.Via {
		case "", ToolViaOff:
			if e.Via == ToolViaOff {
				if e.Server != "" || len(e.Only) > 0 || len(e.Except) > 0 || len(e.Tools) > 0 || len(e.Paths) > 0 || len(e.Limits) > 0 {
					return fmt.Errorf("tools.%s: off takes no extra fields", name)
				}
			}
		case ToolViaNative:
			if !nativeCarveoutToolsets[name] && !pseudoToolsets[name] {
				return fmt.Errorf("tools.%s: %q is not a reviewed native toolset", name, name)
			}
			if pseudoToolsets[name] && managed && name == "meet" {
				return fmt.Errorf("tools.%s: meet needs plugin support sealed under managed mode", name)
			}
			if e.Server != "" || len(e.Only) > 0 || len(e.Except) > 0 || len(e.Paths) > 0 || len(e.Limits) > 0 {
				return fmt.Errorf("tools.%s: native grants are whole-toolset — only per-tool `tools:` toggles are allowed", name)
			}
		case ToolViaToolHub:
			if !toolhubFamilies[name] {
				return fmt.Errorf("tools.%s: %q is not a ToolHub capability family", name, name)
			}
			if e.Server != "" {
				return fmt.Errorf("tools.%s: server is only valid with via: mcp", name)
			}
		case ToolViaMCP:
			_, custom := s.MCP[name]
			if !mcpConnectors[name] && !custom {
				return fmt.Errorf("tools.%s: unknown MCP capability — use mcp:<name> or define mcp_servers.%s", name, name)
			}
			if e.Server != "" {
				if _, ok := s.MCP[e.Server]; !ok && e.Server != "playwright" {
					return fmt.Errorf("tools.%s: unknown MCP server %q", name, e.Server)
				}
			}
		default:
			return fmt.Errorf("tools.%s: unknown backend %q (native|toolhub|mcp[:server]|off)", name, e.Via)
		}
		if e.Via != "" && e.Via != ToolViaOff && e.Via != ToolViaNative && len(e.Tools) > 0 {
			tokens, ok := toolToggleTokens[name]
			for token := range e.Tools {
				if !toolTogglePattern.MatchString(token) {
					return fmt.Errorf("tools.%s: invalid tool toggle %q", name, token)
				}
				if ok && !tokens[token] {
					return fmt.Errorf("tools.%s: unknown toggle %q", name, token)
				}
			}
		}
		for _, list := range [][]string{e.Only, e.Except} {
			for _, tool := range list {
				if !toolTogglePattern.MatchString(tool) {
					return fmt.Errorf("tools.%s: invalid tool name %q", name, tool)
				}
			}
		}
		for _, p := range e.Paths {
			if !toolPathPattern.MatchString(p) || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
				return fmt.Errorf("tools.%s: invalid path prefix %q", name, p)
			}
		}
		for key, value := range e.Limits {
			if !toolLimitPattern.MatchString(key) || value <= 0 {
				return fmt.Errorf("tools.%s: invalid limit %q=%d", name, key, value)
			}
		}
	}
	for _, channel := range s.Ingress {
		if !ingressChannels[channel] {
			return fmt.Errorf("unknown ingress channel %q", channel)
		}
	}
	return nil
}
