// Package passportview is the leaf read path for published six-passport
// revisions: typed bodies, strict Decode and LoadEffective. It sits below
// internal/passport (compile/publish) so every consumer — stack, toolhub,
// migration — reads the same contract without an import cycle.
package passportview

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Names is the fixed six-passport order.
var Names = []string{"stack", "config", "models", "workspace", "tools", "grants"}

// Current returns the active revision id.
func Current(spaceDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(spaceDir, "generated", "passports", "current"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Effective is the current revision as typed data — the single read path
// consumers use (R1.3). A revision that cannot be fully decoded, mixes
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

// LoadEffective reads the revision the `current` pointer names and decodes
// all six passports strictly. There is no fallback to legacy files: a space
// without a published revision, or a corrupted one, is an error.
func LoadEffective(spaceDir string) (Effective, error) {
	var eff Effective
	rev, err := Current(spaceDir)
	if err != nil {
		return eff, fmt.Errorf("load effective: %w", err)
	}
	revDir := filepath.Join(spaceDir, "generated", "passports", rev)
	catalogs := ""
	for _, name := range Names {
		b, err := os.ReadFile(filepath.Join(revDir, name+".yaml")) // #nosec G304 -- revision dir is published authority
		if err != nil {
			return eff, fmt.Errorf("load effective %s: %w", name, err)
		}
		doc, err := Decode(name, b)
		if err != nil {
			return eff, err
		}
		h := docHeader(doc)
		if h.Metadata.Revision != rev {
			return eff, fmt.Errorf("load effective: %s carries %s, current is %s", name, h.Metadata.Revision, rev)
		}
		if catalogs == "" {
			catalogs = h.Metadata.Catalogs
		} else if h.Metadata.Catalogs != catalogs {
			return eff, fmt.Errorf("load effective: %s pins catalogs %q, siblings pin %q", name, h.Metadata.Catalogs, catalogs)
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

// apiVersionV2 is the envelope every v2 passport carries. Compile switches
// to it when the envelope emission lands (S0.4); Decode already demands it.
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
}

type ServiceSpec struct {
	Mode string `yaml:"mode"`
}

// config.yaml — runtime, supervision, ports, channels, memory.
type Config struct {
	Runtime     Runtime            `yaml:"runtime"`
	Supervision Supervision        `yaml:"supervision"`
	Ports       map[string]int     `yaml:"ports,omitempty"`
	Channels    map[string]Channel `yaml:"channels,omitempty"`
	Memory      Memory             `yaml:"memory"`
}

type Runtime struct {
	Timezone string         `yaml:"timezone,omitempty"`
	Locale   string         `yaml:"locale,omitempty"`
	Context  RuntimeContext `yaml:"context,omitempty"`
}

type RuntimeContext struct {
	Compaction    string `yaml:"compaction,omitempty"`
	ParallelLanes int    `yaml:"parallel_lanes,omitempty"`
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
	Backend       string `yaml:"backend,omitempty"`
	NightlyRollup string `yaml:"nightly_rollup,omitempty"`
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
	Via      string     `yaml:"via"`
	Endpoint string     `yaml:"endpoint,omitempty"`
	Auth     Auth       `yaml:"auth,omitempty"`
	Sharing  string     `yaml:"sharing,omitempty"`
	Limits   []Limit    `yaml:"limits,omitempty"`
	Models   []ModelRef `yaml:"models,omitempty"`
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
	Tree     string           `yaml:"tree,omitempty"`
	Writable []string         `yaml:"writable,omitempty"`
	ReadOnly []string         `yaml:"read_only,omitempty"`
	Mounts   map[string]Mount `yaml:"mounts,omitempty"`
}

type Mount struct {
	Source string `yaml:"source"`
	Mode   string `yaml:"mode"`
}

// tools.yaml — toolsets in core/extended; never bare tool names.
type Tools struct {
	Core     map[string]Toolset `yaml:"core,omitempty"`
	Extended map[string]Toolset `yaml:"extended,omitempty"`
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
