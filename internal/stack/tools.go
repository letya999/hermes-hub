package stack

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

// The embedded defaults are the fleet profile every space starts from. An
// operator can override either half fleet-wide by dropping a file into
// <hub-root>/policies/agent-default.yaml or policies/workspace-default.yaml —
// Init prefers those over the embedded copies.
//
//go:embed defaults/agent.yaml
var defaultAgentYAML []byte

//go:embed defaults/workspace.yaml
var defaultWorkspaceYAML []byte

// policyTemplate resolves the fleet template for one space file: a
// policies/<name> override beside the spaces/ directory wins, else the
// embedded default.
func policyTemplate(spaceDir, name string, embedded []byte) []byte {
	root := filepath.Dir(spaceDir)
	if filepath.Base(root) == "spaces" {
		root = filepath.Dir(root)
	}
	if b, err := os.ReadFile(filepath.Join(root, "policies", name)); err == nil {
		return b
	}
	return embedded
}

// renderDefaultAgent stamps the agent template with the space identity and
// the allocated host ports while preserving its comments.
func renderDefaultAgent(spaceDir, user, organization string, ports spacePorts) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(policyTemplate(spaceDir, "agent-default.yaml", defaultAgentYAML), &doc); err != nil {
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
		case "oauth_port":
			root.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(ports.oauth), Tag: "!!int"}
		case "browser_port":
			root.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(ports.browser), Tag: "!!int"}
		case "slack_events_port":
			root.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(ports.slack), Tag: "!!int"}
		}
	}
	if !orgSet {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "organization"}, &yaml.Node{Kind: yaml.ScalarNode, Value: organization})
	}
	return yaml.Marshal(&doc)
}

// spacePorts are the host ports Init allocates per space. Ports are an
// allocation, not a preference: hubctl scans sibling spaces and picks the
// lowest free values so two spaces can never collide.
type spacePorts struct {
	oauth, browser, slack int
}

func allocatePorts(spaceDir string) spacePorts {
	ports := spacePorts{oauth: 8000, browser: 6080, slack: 8081}
	root := filepath.Dir(spaceDir)
	if filepath.Base(root) != "spaces" {
		return ports
	}
	used := map[int]bool{}
	if siblings, err := os.ReadDir(root); err == nil {
		for _, sibling := range siblings {
			if !sibling.IsDir() {
				continue
			}
			for _, name := range []string{"agent.yaml", "settings.yaml"} {
				b, err := os.ReadFile(filepath.Join(root, sibling.Name(), name))
				if err != nil {
					continue
				}
				var p struct {
					OAuth   int `yaml:"oauth_port"`
					Browser int `yaml:"browser_port"`
					Slack   int `yaml:"slack_events_port"`
				}
				if yaml.Unmarshal(b, &p) == nil {
					used[p.OAuth], used[p.Browser], used[p.Slack] = true, true, true
				}
			}
		}
	}
	for used[ports.oauth] {
		ports.oauth++
	}
	for used[ports.browser] || ports.browser == ports.oauth {
		ports.browser++
	}
	for used[ports.slack] || ports.slack == ports.oauth || ports.slack == ports.browser {
		ports.slack++
	}
	return ports
}

// ToolEntry is one line of the `tools:` map in workspace.yaml — the single
// operator surface for "how is this capability served". The scalar form is
// just the backend (`terminal: native`); the long form adds the backend,
// read-only mode, per-tool toggles, ToolHub scopes and limits:
//
//	tools:
//	  terminal: native            # upstream toolset inside the runtime
//	  file: toolhub               # managed executor, per-call admission
//	  browser: mcp:playwright     # connector through ToolHub admission
//	  github: mcp                 # connector capability through ToolHub
//	  gitea: mcp-raw              # raw MCP server rendered into Hermes config
//	  code_exec: off              # explicit deny
//	  ssh:
//	    via: toolhub
//	    access: ro                # all mutation toggles forced off
//	  file:
//	    via: toolhub
//	    paths: [docs, inbox]
//	    limits: {output_bytes: 65536}
type ToolEntry struct {
	Via    string          `yaml:"via,omitempty"`
	Server string          `yaml:"server,omitempty"`
	Access string          `yaml:"access,omitempty"` // ro | rw (default)
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
	ToolViaMCPRaw  = "mcp-raw"
	ToolViaOff     = "off"
)

// Access modes. Empty and "rw" are the default; "ro" strips every mutation
// path the backend knows how to strip and is a validation error on backends
// that cannot honour it (native whole-toolset grants, off entries).
const (
	ToolAccessRO = "ro"
	ToolAccessRW = "rw"
)

// UnmarshalYAML accepts either the scalar backend form (`terminal: native`,
// `browser: mcp:playwright`) or the long mapping form.
func (e *ToolEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		via, server := value.Value, ""
		if strings.HasPrefix(via, "mcp-raw:") {
			via, server = ToolViaMCPRaw, strings.TrimPrefix(via, "mcp-raw:")
		} else if strings.HasPrefix(via, "mcp:") {
			via, server = ToolViaMCP, strings.TrimPrefix(via, "mcp:")
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
	"code_exec": true, "web": true, "settings": true,
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
// only — plugins stay sealed under managed), transcription is the stt flag,
// deep_research is the bundled-skill gate.
var pseudoToolsets = map[string]bool{"meet": true, "transcription": true, "deep_research": true}

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

// toolhubCapabilityID maps a `via: toolhub` entry name to the executor
// capability id its tools consume. `access: ro` on an entry lands in the
// rendered HUB_TOOLS_RO env; the agenttools executor denies every
// write-effect tool of a listed capability — a runtime-side gate that does
// not depend on profile wording.
var toolhubCapabilityID = map[string]string{
	"files": "files", "file": "files", "documents": "documents", "docs": "documents",
	"images": "images", "artifacts": "artifacts", "routines": "routines",
	"services": "services", "hh": "hh", "ssh": "ssh", "image_gen": "image_gen",
	"terminal": "terminal", "code_exec": "terminal", "web": "web",
	"settings": "settings",
}

// readOnlyCapabilities lists the ToolHub capability ids whose `access: ro`
// entries force write-effect tools off inside the runtime executor.
func (s Settings) readOnlyCapabilities() []string {
	seen := map[string]bool{}
	out := []string{}
	for name, e := range s.Tools {
		if !e.enabled() || (e.Access != ToolAccessRO && !s.govForcedRO(name)) {
			continue
		}
		if id, ok := toolhubCapabilityID[name]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// mcpRawMutationTools names the state-changing tools of reviewed mcp-raw
// servers so `access: ro` can render a real tools.exclude filter. Servers not
// in this table require an explicit `except:` list — a bare `ro` there would
// be a promise the hub cannot enforce.
var mcpRawMutationTools = map[string][]string{
	"playwright": browserMutationTools,
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

// deniedMountSegments are path components a workspace mount source may never
// contain: host credential stores, space-internal state dirs and the hub's
// own config files. The agent sees only what is mounted — a secrets-bearing
// directory mounted in would bypass every credential boundary.
var deniedMountSegments = map[string]bool{
	".ssh": true, ".aws": true, ".azure": true, ".gcloud": true, ".gnupg": true,
	".kube": true, ".docker": true, ".secrets": true, ".state": true,
	"connections": true, "hermes": true, "runtime": true, "managed": true,
	"generated": true, "cache": true, "home": true,
	"settings.yaml": true, "workspace.yaml": true, "agent.yaml": true,
	"scope.yaml": true, "soul.md": true,
}

// deniedMountBase is the NanoClaw-style blocklist on basenames: credential
// files and env material never enter a container through a declared mount.
var deniedMountBase = regexp.MustCompile(`^(\.env.*|credentials.*|.*_key|.*\.pem|id_rsa.*|id_ed25519.*|id_dsa.*|id_ecdsa.*|private_key.*|\.netrc|\.npmrc|\.kubeconfig|secrets\.(dev|prod)\.env)$`)

func deniedMountSource(from string) bool {
	base := strings.ToLower(filepath.Base(from))
	if deniedMountSegments[base] || deniedMountBase.MatchString(base) {
		return true
	}
	for _, seg := range strings.FieldsFunc(from, func(r rune) bool { return r == '/' || r == '\\' }) {
		if deniedMountSegments[strings.ToLower(seg)] {
			return true
		}
	}
	return false
}

func (w Workspace) validate() error {
	seen := map[string]bool{}
	for _, m := range w.Mounts {
		from := strings.TrimPrefix(m.From, "org:")
		if from == "" || strings.HasPrefix(from, "/") || strings.Contains(from, "..") || strings.ContainsAny(from, "\\\x00") {
			return fmt.Errorf("invalid workspace mount source %q", m.From)
		}
		if deniedMountSource(from) {
			return fmt.Errorf("workspace mount source %q matches the credential/state blocklist", m.From)
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
			if s.toolEntry("terminal").Via == ToolViaToolHub && name == "code_execution" {
				continue
			}
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
	Native   []string          `json:"native"`
	ToolHub  []string          `json:"toolhub"`
	MCP      map[string]string `json:"mcp"`
	MCPRaw   map[string]string `json:"mcp_raw"`
	Off      []string          `json:"off"`
	ReadOnly []string          `json:"read_only"`
	Ingress  []string          `json:"ingress"`
	Mounts   []Mount           `json:"mounts"`
}

// ToolPlan compiles the tools map into per-backend groupings. MCP and MCPRaw
// values name the resolved server (entry.Server, defaulting to the entry).
func (s Settings) ToolPlan() ToolPlan {
	plan := ToolPlan{MCP: map[string]string{}, MCPRaw: map[string]string{}, Mounts: s.Workspace.Mounts, Ingress: append([]string(nil), s.Ingress...)}
	for name, entry := range s.Tools {
		server := entry.Server
		if server == "" {
			server = name
		}
		switch entry.Via {
		case ToolViaNative:
			plan.Native = append(plan.Native, name)
		case ToolViaToolHub:
			plan.ToolHub = append(plan.ToolHub, name)
		case ToolViaMCP:
			plan.MCP[name] = server
		case ToolViaMCPRaw:
			plan.MCPRaw[name] = server
		case ToolViaOff:
			plan.Off = append(plan.Off, name)
		}
		if entry.Access == ToolAccessRO {
			plan.ReadOnly = append(plan.ReadOnly, name)
		}
	}
	slices.Sort(plan.Native)
	slices.Sort(plan.ToolHub)
	slices.Sort(plan.Off)
	slices.Sort(plan.ReadOnly)
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

// mcpRawSelector finds the tools entry that explicitly selects a raw MCP
// server definition — either by name (`gitea: mcp-raw`) or by `server:` —
// and reports the entry name so governance caps follow the right key.
func mcpRawSelector(s Settings, serverName string) (string, ToolEntry, bool) {
	for name, e := range s.Tools {
		if e.Via != ToolViaMCPRaw {
			continue
		}
		server := e.Server
		if server == "" {
			server = name
		}
		if server == serverName {
			return name, e, true
		}
	}
	return "", ToolEntry{}, false
}

// rawMCPServers renders the mcp: definitions selected for this runtime.
// Unmanaged mode keeps the historical implicit surface (every definition not
// explicitly denied renders); managed mode renders only definitions picked
// by a via: mcp-raw entry, which can only name organization-provided servers.
// `access: ro` and `except:` merge into the server's tools.exclude filter.
func rawMCPServers(s Settings) M {
	servers := M{}
	managed := s.CapabilityMode == "managed"
	// The whole declaration surface is one governance section: user_mcp on a
	// personal space, org_mcp for the merged organization catalog.
	section := toolhub.SectionUserMCP
	if s.OrgScoped() {
		section = toolhub.SectionOrgMCP
	}
	if !s.govAllows(section) {
		return servers
	}
	for name, server := range s.MCP {
		if s.toolEntry(name).Via == ToolViaOff {
			continue
		}
		entryName, e, selected := mcpRawSelector(s, name)
		if managed && !selected {
			continue
		}
		cfg := server.Config()
		exclude := slices.Clone(e.Except)
		if e.Access == ToolAccessRO || s.govForcedRO(entryName) {
			exclude = append(exclude, mcpRawMutationTools[name]...)
		}
		if server.Tools != nil {
			exclude = append(exclude, server.Tools.Exclude...)
		}
		if len(exclude) > 0 {
			tools := MCPTools{Exclude: exclude}
			if server.Tools != nil {
				tools.Include = server.Tools.Include
			}
			cfg["tools"] = tools
		}
		servers[name] = cfg
	}
	return servers
}

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
	// access: ro forces every mutation toggle off — all named toggles are
	// write-paths, so read-only means none of them may be asserted.
	return e.enabled() && e.Access != ToolAccessRO && e.Tools[token]
}

// Has reports feature enablement in the pre-`tools:` vocabulary so every
// render, doctor and scope call site keeps one lookup. Derived, never stored.
func (s Settings) Has(name string) bool {
	switch name {
	case "workspace":
		return true
	case "telegram", "slack_app":
		return slices.Contains(s.Ingress, name)
	case "telegram_auth":
		return s.TelegramAuth
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
	if s.toolEntry("terminal").Via == ToolViaToolHub && s.toolEntry("code_execution").Via == ToolViaNative {
		return fmt.Errorf("terminal: toolhub conflicts with native code_execution; use the networkless code_exec tool")
	}
	managed := s.CapabilityMode == "managed"
	for name, e := range s.Tools {
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("invalid tools entry name %q", name)
		}
		if e.Access != "" && e.Access != ToolAccessRO && e.Access != ToolAccessRW {
			return fmt.Errorf("tools.%s: access must be ro or rw", name)
		}
		switch e.Via {
		case "", ToolViaOff:
			if e.Via == ToolViaOff {
				if e.Server != "" || e.Access != "" || len(e.Only) > 0 || len(e.Except) > 0 || len(e.Tools) > 0 || len(e.Paths) > 0 || len(e.Limits) > 0 {
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
			if e.Access == ToolAccessRO {
				return fmt.Errorf("tools.%s: access ro cannot apply to native — upstream toolsets are granted whole", name)
			}
			if e.Server != "" || len(e.Only) > 0 || len(e.Except) > 0 || len(e.Paths) > 0 || len(e.Limits) > 0 {
				return fmt.Errorf("tools.%s: native grants are whole-toolset — only per-tool `tools:` toggles are allowed", name)
			}
		case ToolViaToolHub:
			if !toolhubFamilies[name] {
				return fmt.Errorf("tools.%s: %q is not a ToolHub capability family", name, name)
			}
			if e.Server != "" {
				return fmt.Errorf("tools.%s: server is only valid with via: mcp or mcp-raw", name)
			}
		case ToolViaMCP:
			if !mcpConnectors[name] {
				return fmt.Errorf("tools.%s: %q is not a ToolHub connector — a raw mcp: server uses via: mcp-raw", name, name)
			}
			if e.Server != "" && e.Server != "playwright" {
				return fmt.Errorf("tools.%s: unknown connector server %q", name, e.Server)
			}
		case ToolViaMCPRaw:
			server := e.Server
			if server == "" {
				server = name
			}
			// Organization servers merge into s.MCP only after Read (marked by
			// OrganizationDir): before the merge resolution is deferred to the
			// post-merge Validate in ReadEnvironment.
			if _, ok := s.MCP[server]; !ok && !(s.OrgScoped() && s.OrganizationDir == "") {
				return fmt.Errorf("tools.%s: mcp-raw needs a server defined in mcp: (%q)", name, server)
			}
			if e.Access == ToolAccessRO && len(mcpRawMutationTools[server]) == 0 && len(e.Except) == 0 {
				return fmt.Errorf("tools.%s: access ro on mcp-raw needs a reviewed mutation table or an explicit except: list", name)
			}
		default:
			return fmt.Errorf("tools.%s: unknown backend %q (native|toolhub|mcp[:server]|mcp-raw|off)", name, e.Via)
		}
		if e.Access == ToolAccessRO && len(e.Tools) > 0 {
			for token, on := range e.Tools {
				if on && toolToggleTokens[name][token] {
					return fmt.Errorf("tools.%s: access ro conflicts with tools.%s: true", name, token)
				}
			}
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
