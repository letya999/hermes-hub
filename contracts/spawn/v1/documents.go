// This file carries the typed six-passport v2 schema — the decode surface
// every consumer of EffectiveSpec.Documents and PolicySlice.Documents
// shares. It moved here from internal/passportview (the deleted v0 file
// reader): the bundle documents ride the contract, so their typed view
// belongs to the contract, not to a filesystem package.
package spawn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Names is the fixed six-passport order.
var Names = []string{"stack", "config", "models", "workspace", "tools", "grants"}

// Effective is the active revision as typed data — the single read path
// consumers use (R1.3). A bundle that cannot be fully decoded, mixes
// revision ids or pins different catalog releases never partially loads.
type Effective struct {
	Header    Header
	Stack     Stack
	Config    Config
	Models    Models
	Workspace Workspace
	Tools     Tools
	Grants    Grants
}

// Assemble decodes one stored six-document policy bundle — the spawn
// service's effective_specs row — into the typed effective view. The
// bundle's JSON documents carry the full envelope, so Decode's strict
// checks apply unchanged; the same cross-document
// revision/space/environment/catalogs fence the old file path enforced
// applies here too.
func Assemble(docs map[string]json.RawMessage) (Effective, error) {
	var eff Effective
	rev, space, environment, catalogs := "", "", "", ""
	for _, name := range Names {
		raw, ok := docs[name]
		if !ok {
			return eff, fmt.Errorf("load effective %s: document missing", name)
		}
		doc, err := Decode(name, raw)
		if err != nil {
			return eff, err
		}
		h := docHeader(doc)
		if rev == "" {
			rev, space, environment, catalogs = h.Metadata.Revision, h.Metadata.Space, h.Metadata.Environment, h.Metadata.Catalogs
		} else {
			if h.Metadata.Revision != rev {
				return eff, fmt.Errorf("load effective: %s carries %s, siblings carry %s", name, h.Metadata.Revision, rev)
			}
			if h.Metadata.Space != space || h.Metadata.Environment != environment {
				return eff, fmt.Errorf("load effective: %s belongs to a different space or environment", name)
			}
			if h.Metadata.Catalogs != catalogs {
				return eff, fmt.Errorf("load effective: %s pins catalogs %q, siblings pin %q", name, h.Metadata.Catalogs, catalogs)
			}
		}
		eff.Header = h
		switch d := doc.(type) {
		case *StackDoc:
			eff.Stack = d.Stack
		case *ConfigDoc:
			eff.Config = d.Config
		case *ModelsDoc:
			eff.Models = d.Models
		case *WorkspaceDoc:
			eff.Workspace = d.Workspace
		case *ToolsDoc:
			eff.Tools = d.Tools
		case *GrantsDoc:
			eff.Grants = d.Grants
		}
	}
	return eff, nil
}

// PromptPointer renders the short per-turn inventory line R9.1 injects: the
// revision id, the bounded core tool names and the extended count, plus the
// pinned platform pointer. It never embeds catalog bodies — tool_search keeps
// discovery lazy inside the turn.
func (e Effective) PromptPointer() string {
	names := make([]string, 0, len(e.Tools.Core))
	for k := range e.Tools.Core {
		if k != "platform" && k != "discovery" {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return fmt.Sprintf("revision=%s core=%s extended=+%d platform=tool_search+tool_call\n",
		e.Header.Metadata.Revision, strings.Join(names, ","), len(e.Tools.Extended))
}

// APIVersionV2 is the envelope every v2 passport carries.
const APIVersionV2 = "six-passport/v2"

// Header is the common v2 envelope: kind carries the passport name and the
// short metadata block keeps space, environment, revision, generation time,
// authoring template and the pinned catalog release. Provenance fields
// (updated_at, sources) live in the revision's provenance.yaml instead.
type Header struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
}

// Metadata is the v2 file envelope (SPEC-0092 normative header).
type Metadata struct {
	Space       string    `yaml:"space"`
	Environment string    `yaml:"environment"`
	Revision    string    `yaml:"revision"`
	GeneratedAt time.Time `yaml:"generated_at"`
	Template    string    `yaml:"template,omitempty"`
	Catalogs    string    `yaml:"catalogs"`
}

// stack.yaml — which containers and images run, and in which mode.
type Stack struct {
	Identity Identity               `yaml:"identity"`
	Services map[string]ServiceSpec `yaml:"services"`
}

type Identity struct {
	User         string `yaml:"user"`
	Organization string `yaml:"organization,omitempty"`
	// Kind is the provisioning-stamped scope kind ("user" or "group"); an
	// absent kind is a personal user space. It is immutable after the first
	// publication — a scope's kind cannot be renegotiated by editing files.
	Kind  string `yaml:"kind,omitempty"`
	Infra *bool  `yaml:"infra,omitempty"`
}

type ServiceSpec struct {
	Mode string `yaml:"mode"`
}

// config.yaml — runtime, supervision, ports, channels, memory.
type Config struct {
	Runtime     RuntimeConfig      `yaml:"runtime"`
	Supervision Supervision        `yaml:"supervision"`
	Ports       map[string]int     `yaml:"ports,omitempty"`
	Channels    map[string]Channel `yaml:"channels,omitempty"`
	Memory      Memory             `yaml:"memory"`
	Web         Web                `yaml:"web,omitempty"`
	Hosting     *Hosting           `yaml:"hosting,omitempty"`
	// ImageGen is the image-generation provider grant the retired
	// settings.yaml image_gen section carried (provider/model/delivery).
	ImageGen ImageGen `yaml:"image_gen,omitempty"`
	// IAM is the optional SSO identity contract (ADR-0100): an external
	// OIDC provider, or the bundled identity-hub sidecar pair
	// (stack.yaml services identity-hub + hub-postgres, mode shared).
	// Absent keeps the deployment in channel-mapped local mode.
	IAM *IAM `yaml:"iam,omitempty"`
}

// ImageGen is the image-generation provider/model/delivery triple the config
// passport carries. It duplicates media.ImageGen's wire shape so the contract
// package stays free of the media stack's codec dependencies.
type ImageGen struct {
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Delivery string `yaml:"delivery,omitempty"`
}

// IAM carries the SSO provider selection. external points the shared
// gateway at any reachable OIDC issuer; bundled renders the stack-managed
// identity-hub (ZITADEL) on the shared postgres — issuer is then optional:
// absent means the dev shape http://idp.localhost:<port>, present is the
// public https issuer behind its own DNS/TLS.
type IAM struct {
	Mode     string `yaml:"mode"`
	Issuer   string `yaml:"issuer,omitempty"`
	ClientID string `yaml:"client_id,omitempty"`
}

// Web carries the managed web policy without importing the stack renderer.
type Web struct {
	Backend          string            `yaml:"backend,omitempty"`
	SearchBackend    string            `yaml:"search_backend,omitempty"`
	ExtractBackend   string            `yaml:"extract_backend,omitempty"`
	SearchProviders  []string          `yaml:"search_providers,omitempty"`
	KeylessFallback  *bool             `yaml:"keyless_fallback,omitempty"`
	KeylessRescue    *bool             `yaml:"keyless_rescue,omitempty"`
	ExtractCharLimit int               `yaml:"extract_char_limit,omitempty"`
	ProviderTier     map[string]string `yaml:"provider_tier,omitempty"`
	CacheEnabled     *bool             `yaml:"cache_enabled,omitempty"`
	CacheTTLMinutes  int               `yaml:"cache_ttl_minutes,omitempty"`
	CacheExemptHosts []string          `yaml:"cache_exempt_hosts,omitempty"`
	Research         WebResearch       `yaml:"research,omitempty"`
}

type WebResearch struct {
	MaxRounds       int `yaml:"max_rounds,omitempty"`
	QueriesPerRound int `yaml:"queries_per_round,omitempty"`
	PagesPerRound   int `yaml:"pages_per_round,omitempty"`
	MaxPages        int `yaml:"max_pages,omitempty"`
	MaxPagesPerHost int `yaml:"max_pages_per_host,omitempty"`
	DeadlineMinutes int `yaml:"deadline_minutes,omitempty"`
}

// Hosting is the site-hosting contract: unique safe slots and an optional
// digest-pinned sandbox image with its explicit runtime.
type Hosting struct {
	Slots   []string `yaml:"slots,omitempty"`
	Image   string   `yaml:"image,omitempty"`
	Runtime string   `yaml:"runtime,omitempty"`
}

// RuntimeConfig is the config.yaml runtime: section; named RuntimeConfig so
// the type shares this package with the spawn Runtime DTO.
type RuntimeConfig struct {
	Timezone    string           `yaml:"timezone,omitempty"`
	Locale      string           `yaml:"locale,omitempty"`
	Context     RuntimeContext   `yaml:"context,omitempty"`
	Goals       RuntimeGoals     `yaml:"goals,omitempty"`
	Compression map[string]any   `yaml:"compression,omitempty"`
	Auxiliary   RuntimeAuxiliary `yaml:"auxiliary,omitempty"`
	// Introspection gates operator hubctl exec/chat/meet-auth. Nil keeps the
	// personal-space default; group and organization scopes stay opt-in.
	Introspection *bool `yaml:"introspection,omitempty"`
}

type RuntimeContext struct {
	Compaction    string   `yaml:"compaction,omitempty"`
	Engines       []string `yaml:"engines,omitempty"`
	ParallelLanes int      `yaml:"parallel_lanes,omitempty"`
}

// RuntimeGoals mirrors the retired settings.yaml goals: block — engine,
// judge and turn bound for the managed goals surface.
type RuntimeGoals struct {
	Engine   string `yaml:"engine,omitempty"`
	Judge    string `yaml:"judge,omitempty"`
	MaxTurns int    `yaml:"max_turns,omitempty"`
}

// RuntimeAuxiliary names the logical models side paths (summary, judge)
// resolve through the owner's model relay.
type RuntimeAuxiliary struct {
	SummaryModel string `yaml:"summary_model,omitempty"`
	JudgeModel   string `yaml:"judge_model,omitempty"`
}

type Supervision struct {
	IdleStop string             `yaml:"idle_stop,omitempty"`
	Restart  string             `yaml:"restart,omitempty"`
	Watchers map[string]Watcher `yaml:"watchers,omitempty"`
}

type Watcher struct {
	Protected bool   `yaml:"protected,omitempty"`
	Every     string `yaml:"every,omitempty"`
}

// Channel carries credential references only — never secret bytes.
type Channel struct {
	Bot          Ref   `yaml:"bot,omitempty"`
	AllowedUsers Ref   `yaml:"allowed_users,omitempty"`
	Voice        Voice `yaml:"voice,omitempty"`
}

type Ref struct {
	Ref string `yaml:"ref"`
}

type Voice struct {
	In  string `yaml:"in,omitempty"`
	Out string `yaml:"out,omitempty"`
}

type Memory struct {
	Enabled           *bool           `yaml:"enabled,omitempty"`
	Backend           string          `yaml:"backend,omitempty"`
	NightlyRollup     string          `yaml:"nightly_rollup,omitempty"`
	LeafRetentionDays int             `yaml:"leaf_retention_days,omitempty"`
	LeafFoldBytes     int             `yaml:"leaf_fold_bytes,omitempty"`
	Provider          *MemoryProvider `yaml:"provider,omitempty"`
}

// MemoryProvider is the external memory-backend endpoint record the retired
// settings memory_provider* fields carried — adapter kind, absolute URL and
// the secrets variable holding its token.
type MemoryProvider struct {
	Adapter  string `yaml:"adapter"`
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env,omitempty"`
}

// models.yaml — routes, ladders and per-service auth contracts.
type Models struct {
	Default  string                  `yaml:"default,omitempty"`
	Routes   map[string]Route        `yaml:"routes,omitempty"`
	Services map[string]ModelService `yaml:"services,omitempty"`
}

type Route struct {
	Ladder []string `yaml:"ladder"`
}

type ModelService struct {
	Via          string     `yaml:"via"`
	Endpoint     string     `yaml:"endpoint,omitempty"`
	AllowPrivate bool       `yaml:"allow_private,omitempty"`
	Auth         Auth       `yaml:"auth,omitempty"`
	Sharing      string     `yaml:"sharing,omitempty"`
	Limits       []Limit    `yaml:"limits,omitempty"`
	Models       []ModelRef `yaml:"models,omitempty"`
}

// Auth names broker contract refs; secret bytes never enter the passport.
type Auth struct {
	Upstream    *AuthRef `yaml:"upstream,omitempty"`
	ProxyAccess *AuthRef `yaml:"proxy_access,omitempty"`
}

type AuthRef struct {
	Kind string `yaml:"kind"`
	Ref  string `yaml:"ref"`
}

type Limit struct {
	Of     string `yaml:"of"`
	Amount int    `yaml:"amount"`
	Unit   string `yaml:"unit,omitempty"`
	Per    string `yaml:"per"`
	Clock  string `yaml:"clock,omitempty"`
}

type ModelRef struct {
	ID        string `yaml:"id"`
	Reasoning string `yaml:"reasoning,omitempty"`
}

// workspace.yaml — one ASCII tree, boundary lists and external mounts.
type Workspace struct {
	Tree              string           `yaml:"tree,omitempty"`
	Writable          []string         `yaml:"writable,omitempty"`
	ReadOnly          []string         `yaml:"read_only,omitempty"`
	Mounts            map[string]Mount `yaml:"mounts,omitempty"`
	Goal              string           `yaml:"goal,omitempty"`
	BootstrapRevision uint64           `yaml:"bootstrap_revision,omitempty"`
}

type Mount struct {
	Source string `yaml:"source"`
	Mode   string `yaml:"mode"`
}

// tools.yaml — toolsets in core/extended; never bare tool names.
type Tools struct {
	Core     map[string]Toolset `yaml:"core,omitempty"`
	Extended map[string]Toolset `yaml:"extended,omitempty"`
	Binding  *ToolBinding       `yaml:"binding,omitempty"`
	// Connectors declares raw MCP upstreams (the retired workspace mcp:
	// surface): the user_mcp/org_mcp uncurated toolsets admit this map as
	// their member set — it is not reachable unless such a toolset is
	// selected and its section right is granted.
	Connectors map[string]Connector `yaml:"connectors,omitempty"`
}

// Connector is one owner-declared raw MCP upstream — an external
// connection, not an embedded service. Fields mirror the retired
// workspace.yaml server definition one to one.
type Connector struct {
	URL     string            `yaml:"url,omitempty"`
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Tools   *ConnectorTools   `yaml:"tools,omitempty"`
	Auth    string            `yaml:"auth,omitempty"`
	Timeout int               `yaml:"timeout,omitempty"`
}

// ConnectorTools is the per-server tool filter the runtime merges with
// governance-driven exclusions.
type ConnectorTools struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

type ToolBinding struct {
	ProfileID  string `yaml:"profile_id"`
	Generation uint64 `yaml:"generation"`
}

type Toolset struct {
	Via    string          `yaml:"via"`
	Tools  *Selector       `yaml:"tools,omitempty"`
	Access string          `yaml:"access,omitempty"`
	Scope  *Scope          `yaml:"scope,omitempty"`
	Rules  map[string]Rule `yaml:"rules,omitempty"`
}

type Selector struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

type Scope struct {
	Paths []string `yaml:"paths,omitempty"`
	Hosts []string `yaml:"hosts,omitempty"`
	URLs  []string `yaml:"urls,omitempty"`
}

type Rule struct {
	Scope *Scope `yaml:"scope,omitempty"`
}

// grants.yaml — agent rights on scopes: capability -> allowed operations.
type Grants struct {
	Allow map[string][]string `yaml:"allow,omitempty"`
}

// The six v2 documents embed the header inline so contract keys stay
// top-level while strict decode still rejects unknown fields.
type (
	StackDoc struct {
		Header `yaml:",inline"`
		Stack  `yaml:",inline"`
	}
	ConfigDoc struct {
		Header `yaml:",inline"`
		Config `yaml:",inline"`
	}
	ModelsDoc struct {
		Header `yaml:",inline"`
		Models `yaml:",inline"`
	}
	WorkspaceDoc struct {
		Header    `yaml:",inline"`
		Workspace `yaml:",inline"`
	}
	ToolsDoc struct {
		Header `yaml:",inline"`
		Tools  `yaml:",inline"`
	}
	GrantsDoc struct {
		Header `yaml:",inline"`
		Grants `yaml:",inline"`
	}
)

func docHeader(doc any) Header {
	switch d := doc.(type) {
	case *StackDoc:
		return d.Header
	case *ConfigDoc:
		return d.Header
	case *ModelsDoc:
		return d.Header
	case *WorkspaceDoc:
		return d.Header
	case *ToolsDoc:
		return d.Header
	case *GrantsDoc:
		return d.Header
	}
	return Header{}
}

// Decode parses one published passport by name. It fails closed on an
// unknown key, a kind/apiVersion mismatch, a missing revision or a second
// document — a malformed passport is never half-read.
func Decode(name string, b []byte) (any, error) {
	var doc any
	switch name {
	case "stack":
		doc = &StackDoc{}
	case "config":
		doc = &ConfigDoc{}
	case "models":
		doc = &ModelsDoc{}
	case "workspace":
		doc = &WorkspaceDoc{}
	case "tools":
		doc = &ToolsDoc{}
	case "grants":
		doc = &GrantsDoc{}
	default:
		return nil, fmt.Errorf("passport decode: unknown passport %q", name)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil {
		return nil, fmt.Errorf("passport decode %s: %w", name, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("passport decode %s: trailing document", name)
	}
	h := docHeader(doc)
	if h.APIVersion != APIVersionV2 || h.Kind != name || h.Metadata.Revision == "" {
		return nil, fmt.Errorf("passport decode %s: envelope mismatch (apiVersion=%q kind=%q revision=%q)",
			name, h.APIVersion, h.Kind, h.Metadata.Revision)
	}
	return doc, nil
}

// EncodeDocuments re-encodes a typed effective bundle into its six YAML
// envelopes — the byte form Revision.Docs and the /config view carry. Each
// document's kind is its own; the shared metadata block is identical across
// the bundle, exactly the invariant Assemble enforces on decode.
func EncodeDocuments(eff Effective) (map[string][]byte, error) {
	header := func(kind string) Header {
		return Header{APIVersion: APIVersionV2, Kind: kind, Metadata: eff.Header.Metadata}
	}
	docs := map[string]any{
		"stack":     StackDoc{Header: header("stack"), Stack: eff.Stack},
		"config":    ConfigDoc{Header: header("config"), Config: eff.Config},
		"models":    ModelsDoc{Header: header("models"), Models: eff.Models},
		"workspace": WorkspaceDoc{Header: header("workspace"), Workspace: eff.Workspace},
		"tools":     ToolsDoc{Header: header("tools"), Tools: eff.Tools},
		"grants":    GrantsDoc{Header: header("grants"), Grants: eff.Grants},
	}
	out := map[string][]byte{}
	for name, doc := range docs {
		body, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", name, err)
		}
		out[name] = body
	}
	return out, nil
}
