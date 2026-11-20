// Package passport compiles one atomic six-passport revision for a managed
// space (ADR-0092 D1-D3, SPEC-0091 R2). Platform ceiling -> pinned platform
// template -> narrower org ceiling -> versioned org template -> user choice
// resolve into six documents sharing one envelope: space, environment,
// revision, timestamps, template, sources and the pinned catalog versions.
package passport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/shared/locale"
)

// Names are the six passports one revision publishes together.

// APIVersion is the common envelope every passport file carries (v2:
// kind is the passport name, contract keys are top-level).
const APIVersion = passportview.APIVersionV2

// Layer is one versioned source document: passport name -> field tree.
type Layer struct {
	Name    string                    `yaml:"name"`
	Version int                       `yaml:"version"`
	Docs    map[string]map[string]any `yaml:"docs"`
}

func (l Layer) present() bool { return l.Name != "" || len(l.Docs) > 0 }

// Inputs are everything a revision is compiled from: the layered sources,
// the trusted ledger snapshot grants are projected from, and the pinned
// platform catalogs cross-file links validate against. Now is injectable so
// identical inputs compile deterministically.
type Inputs struct {
	Space            string
	Environment      string
	PlatformCeiling  Layer
	PlatformTemplate Layer
	OrgCeiling       Layer
	OrgTemplate      Layer
	User             Layer
	// Agent merges last, below the user layer (ADR-0106): it may only
	// narrow the spec the layers above produced — a leaf must already
	// exist, a list may shrink to a subset, a scalar must repeat the
	// value. Empty means the space has no agent overlay.
	Agent   Layer
	Grants  map[string][]string
	Catalog catalog.Docs
	Now     time.Time
	// RendersInfra marks the space that owns the shared control plane —
	// only it may publish a shared service's host port. ReservedPorts
	// carries the host ports sibling spaces already published
	// (port -> space name) so a cross-space collision fails at compile
	// rather than at docker-proxy bind.
	RendersInfra  bool
	ReservedPorts map[int]string
}

// Provenance is the common record every passport revision carries: which
// source layers, templates and catalog releases produced it. The v2 file
// envelope keeps only the short passport.Metadata block — this record is
// emitted as the revision's provenance.yaml (SPEC-0092 R0.2).
type Provenance struct {
	Space       string            `yaml:"space"`
	Environment string            `yaml:"environment"`
	Revision    string            `yaml:"revision"`
	UpdatedAt   time.Time         `yaml:"updated_at"`
	GeneratedAt time.Time         `yaml:"generated_at"`
	Template    map[string]string `yaml:"template"`
	Sources     map[string]int    `yaml:"sources"`
	Catalogs    map[string]string `yaml:"catalogs"`
}

// Revision is one compiled snapshot: the six serialized passports plus the
// provenance record they share.
type Revision struct {
	ID         string
	Provenance Provenance
	Docs       map[string][]byte
}

// fieldClass marks how a layer may set a schema field: invariant fields
// only the platform may fix (deeper layers may only repeat the value),
// narrow fields may only shrink inside the bound above, choice fields pick
// inside the offered set, and user_data fields are the owner's own.
type fieldClass int

const (
	classInvariant fieldClass = iota
	classNarrow
	classChoice
	classUserData
)

// classes is the shared field schema (ADR-0093 D2): every dotted leaf
// path a layer may set, in the v2 normative vocabulary. A path absent
// from the registry is a schema gap no layer may write; "map.*" entries
// are keyset-narrow — a ceiling layer's key set bounds deeper layers.
var classes = map[string]map[string]fieldClass{
	"stack": {
		"identity.user":         classInvariant,
		"identity.organization": classInvariant,
		"identity.kind":         classInvariant,
		"identity.infra":        classInvariant,
		"services.*":            classKeysetNarrow,
	},
	"config": {
		"runtime.timezone":                classUserData,
		"runtime.locale":                  classUserData,
		"runtime.context.compaction":      classChoice,
		"runtime.context.engines":         classChoice,
		"runtime.context.parallel_lanes":  classUserData,
		"runtime.goals.engine":            classChoice,
		"runtime.goals.judge":             classChoice,
		"runtime.goals.max_turns":         classUserData,
		"runtime.compression.*":           classUserData,
		"runtime.auxiliary.summary_model": classUserData,
		"runtime.auxiliary.judge_model":   classUserData,
		"supervision.idle_stop":           classChoice,
		"supervision.restart":             classChoice,
		"supervision.watchers.*":          classKeysetNarrow,
		"ports.*":                         classUserData,
		"channels.*":                      classKeysetNarrow,
		"memory.backend":                  classChoice,
		"memory.enabled":                  classChoice,
		"memory.nightly_rollup":           classChoice,
		"memory.leaf_retention_days":      classChoice,
		"memory.leaf_fold_bytes":          classChoice,
		"memory.provider.adapter":         classChoice,
		"memory.provider.url":             classUserData,
		"memory.provider.token_env":       classUserData,
		"hosting.slots":                   classUserData,
		"hosting.image":                   classUserData,
		"hosting.runtime":                 classChoice,
		"image_gen.provider":              classChoice,
		"image_gen.model":                 classChoice,
		"image_gen.delivery":              classChoice,
		// iam.mode is a topology choice an org ceiling may bound; the
		// issuer and client id are operator values, never layered policy.
		"iam.mode":      classChoice,
		"iam.issuer":    classUserData,
		"iam.client_id": classUserData,
		"web.*":         classUserData,
	},
	"models": {
		"default":    classUserData,
		"routes.*":   classKeysetNarrow,
		"services.*": classKeysetNarrow,
	},
	"workspace": {
		"writable":           classNarrow,
		"read_only":          classNarrow,
		"layout":             classNarrow,
		"mounts.*":           classKeysetNarrow,
		"goal":               classUserData,
		"bootstrap_revision": classUserData,
	},
	"tools": {
		"core.*":             classKeysetNarrow,
		"extended.*":         classKeysetNarrow,
		"connectors.*":       classKeysetNarrow,
		"binding.profile_id": classInvariant,
		"binding.generation": classInvariant,
	},
	"grants": {
		"allow": classNarrow,
	},
}

// flatten maps a layer doc tree to dotted leaf paths.
func flatten(tree map[string]any, prefix string, out map[string]any) {
	for k, v := range tree {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if m, ok := v.(map[string]any); ok {
			flatten(m, path, out)
			continue
		}
		out[path] = v
	}
}

// Compile resolves the layered sources into one six-passport revision.
// Every rule failure is a compile error before a single byte is staged.
func Compile(in Inputs) (Revision, error) {
	var rev Revision
	layers := []Layer{in.PlatformCeiling, in.PlatformTemplate, in.OrgCeiling, in.OrgTemplate, in.User, in.Agent}
	for i, l := range layers {
		for doc := range l.Docs {
			if doc == "grants" && (i == 1 || i == 3) {
				return rev, fmt.Errorf("passport compile: template layer %q must not carry grants", l.Name)
			}
		}
	}
	// Effective value: the deepest literal wins, inside the bound above.
	// Invariant fields are fixed by the first layer that sets them; choice
	// fields pick inside the deepest offered list — a list value narrows
	// the offered set, a scalar chooses one member of it.
	keyBounds, err := keysetBounds(layers)
	if err != nil {
		return rev, err
	}
	bodies := map[string]map[string]any{}
	for _, doc := range Names {
		body := map[string]any{}
		invariants := map[string]any{}
		offered := map[string][]any{}
		bounds := map[string][]any{}
		for i, l := range layers {
			leaves := map[string]any{}
			if tree, ok := l.Docs[doc]; ok {
				flatten(tree, "", leaves)
			}
			// The agent layer (always the last slice entry) narrows the
			// spec the layers above produced — the merged body itself is
			// its bound, so a template-narrowed list stays binding even
			// when no ceiling re-pinned it. For a keyset map the agent's
			// declared keys become the final key set: absence drops the
			// entry, an unknown key is a widening add.
			if i == len(layers)-1 {
				above := map[string]any{}
				flatten(body, "", above)
				agentKeys := map[string]map[string]bool{}
				for p := range leaves {
					class, base, ok := leafClass(doc, p)
					if !ok {
						return rev, fmt.Errorf("passport compile: unclassified field %s.%s", doc, p)
					}
					if class == classKeysetNarrow {
						if key := keyAfter(base, p); key != "" {
							if agentKeys[base] == nil {
								agentKeys[base] = map[string]bool{}
							}
							agentKeys[base][key] = true
						}
					}
				}
				for base, keys := range agentKeys {
					node := mapAt(body, base)
					for k := range node {
						if !keys[k] {
							delete(node, k)
						}
					}
					for k := range keys {
						if _, ok := node[k]; !ok {
							return rev, fmt.Errorf("passport compile: agent layer %q adds %s.%s.%s beyond the user layer", l.Name, doc, base, k)
						}
					}
				}
				for p, raw := range leaves {
					// workspace.layout merges through mergeWorkspaceLayout.
					if doc == "workspace" && p == "layout" {
						continue
					}
					class, _, _ := leafClass(doc, p)
					if err := narrowAgentLeaf(l.Name, doc, p, raw, class, above); err != nil {
						return rev, err
					}
					setPath(body, p, raw)
				}
				continue
			}
			for p, raw := range leaves {
				// workspace.layout merges through mergeWorkspaceLayout —
				// typed nodes narrow by path and mode, not by the generic
				// scalar-membership bound.
				if doc == "workspace" && p == "layout" {
					continue
				}
				class, base, ok := leafClass(doc, p)
				if !ok {
					return rev, fmt.Errorf("passport compile: unclassified field %s.%s", doc, p)
				}
				switch class {
				case classKeysetNarrow:
					bound, bounded := keyBounds[doc+"."+base]
					// Ceiling layers author the bound — only templates and
					// the user layer are checked against it.
					if i == 0 || i == 2 {
						bounded = false
					}
					// core and extended share one toolset vocabulary —
					// placement is a separate choice from admission.
					if doc == "tools" && (base == "core" || base == "extended") {
						other := "extended"
						if base == "extended" {
							other = "core"
						}
						if b2, ok := keyBounds["tools."+other]; ok {
							if bound == nil {
								bound = b2
							} else {
								for k := range b2 {
									bound[k] = true
								}
							}
						}
						bounded = bounded || bound != nil
					}
					// Ceiling layers author the bound — only templates and
					// the user layer are checked against it.
					if i == 0 || i == 2 {
						bounded = false
					}
					if bounded {
						if key := keyAfter(base, p); key != "" && !bound[key] {
							return rev, fmt.Errorf("passport compile: layer %q writes %s.%s outside the ceiling key set", l.Name, doc, p)
						}
					}
				case classNarrow:
					// A narrow field narrows the enclosing ceiling's bound:
					// every listed member must already be offered, and a
					// ceiling layer re-pins the bound for deeper layers.
					list, isList := raw.([]any)
					if !isList {
						return rev, fmt.Errorf("passport compile: narrow field %s.%s expects a list", doc, p)
					}
					if isList {
						if bound, ok := bounds[p]; ok {
							for _, item := range list {
								if !contains(bound, item) {
									return rev, fmt.Errorf("passport compile: layer %q widens %s.%s outside the ceiling", l.Name, doc, p)
								}
							}
						}
						if i == 0 || i == 2 {
							bounds[p] = list
						}
					}
				case classInvariant:
					if fixed, set := invariants[p]; set && fmt.Sprint(fixed) != fmt.Sprint(raw) {
						return rev, fmt.Errorf("passport compile: layer %q widens invariant %s.%s", l.Name, doc, p)
					} else {
						invariants[p] = raw
					}
				case classChoice:
					if choices, bounded := offered[p]; bounded && !contains(choices, raw) {
						return rev, fmt.Errorf("passport compile: layer %q picks %s.%s outside the ceiling", l.Name, doc, p)
					}
					if list, isList := raw.([]any); isList {
						offered[p] = list
					}
				}
				setPath(body, p, raw)
			}
		}
		bodies[doc] = body
	}
	grants, err := projectGrants(in.Grants, in.Catalog, bodies["grants"]["allow"])
	if err != nil {
		return rev, err
	}
	bodies["grants"] = grants
	// The effective workspace boundary is derived before the cross-file
	// checks: layout nodes marked ro, the invariant roots, and ro mount
	// targets join the declared read_only list so checks and the emitted
	// passport see one boundary (SPEC-0092 R5.1).
	nodes, err := mergeWorkspaceLayout(layers)
	if err != nil {
		return rev, err
	}
	roSet := readOnlySet(bodies["workspace"], nodes)
	roList := make([]any, 0, len(roSet))
	sorted := make([]string, 0, len(roSet))
	for p := range roSet {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		roList = append(roList, p)
	}
	bodies["workspace"]["read_only"] = roList
	// The fixed skeleton mounts the vault's library/attachments halves rw —
	// the emitted boundary must admit them even when no layer names them.
	// rw mount targets join too: a mount the kernel allows but the passport
	// denies is an inconsistency. The floor below runs on what the space
	// itself declared (writable list + rw mounts) so a layer granting no
	// writable surface stays a compile error.
	wrList := asList(bodies["workspace"]["writable"])
	have := map[string]bool{}
	for _, p := range wrList {
		have[fmt.Sprint(p)] = true
	}
	for target, raw := range asMap(bodies["workspace"]["mounts"]) {
		if m, _ := raw.(map[string]any); fmt.Sprint(m["mode"]) == "rw" && !have[target] {
			wrList = append(wrList, target)
			have[target] = true
		}
	}
	if len(wrList) == 0 {
		return rev, fmt.Errorf("passport compile: managed revision grants no writable workspace path")
	}
	for _, p := range invariantWritable {
		if have[p] {
			continue
		}
		// A declared read_only above the default narrows it away — only an
		// explicit writable under ro is a contradiction for checkWorkspace.
		shadowed := false
		for ro := range roSet {
			if pathWithin(p, ro) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			wrList = append(wrList, p)
		}
	}
	bodies["workspace"]["writable"] = wrList
	// Cross-file contract checks on the v2 shapes (R3-R7): stack resolves
	// instances and derives dependencies first — models endpoints and
	// config channels reference the running set.
	instances, err := checkStack(bodies, in.Catalog)
	if err != nil {
		return rev, err
	}
	if err := checkWorkspace(bodies); err != nil {
		return rev, err
	}
	if err := checkConfig(bodies, instances, in); err != nil {
		return rev, err
	}
	if err := checkModels(bodies, instances); err != nil {
		return rev, err
	}
	if err := checkTools(bodies, in.Catalog); err != nil {
		return rev, err
	}
	if err := checkComplete(bodies, instances); err != nil {
		return rev, err
	}
	// A watcher a higher layer marked protected keeps that flag in the
	// effective config: user watchers come and go, service watchers stay.
	configLeaves := map[string]any{}
	flatten(bodies["config"], "", configLeaves)
	for _, l := range layers {
		leaves := map[string]any{}
		if tree, ok := l.Docs["config"]; ok {
			flatten(tree, "", leaves)
		}
		for p, raw := range leaves {
			if strings.HasSuffix(p, ".protected") && raw == true {
				if eff := configLeaves[p]; eff != true {
					return rev, fmt.Errorf("passport compile: layer %q drops protected watcher %s", l.Name, p)
				}
			}
		}
	}
	// The ASCII tree is derived from the approved layout, never written by
	// a layer (ADR-0092 D5): the fixed skeleton, layout nodes and mounts
	// render as one box-drawing map. The layout source field itself is not
	// part of the v2 file contract.
	lang := locale.Default
	if rt, ok := bodies["config"]["runtime"].(map[string]any); ok {
		if l, ok := rt["locale"].(string); ok && l != "" {
			lang = locale.Lang(l)
		}
	}
	bodies["workspace"]["tree"] = renderTree(nodes, asMap(bodies["workspace"]["mounts"]), lang)
	delete(bodies["workspace"], "layout")
	rev.Provenance = Provenance{
		Space: in.Space, Environment: in.Environment,
		UpdatedAt: in.Now, GeneratedAt: in.Now,
		Template: map[string]string{}, Sources: map[string]int{},
		Catalogs: map[string]string{
			"capabilities": in.Catalog.Capabilities.Release,
			"toolsets":     in.Catalog.Toolsets.Release,
			"services":     in.Catalog.Services.Release,
		},
	}
	for _, l := range layers {
		if l.present() {
			rev.Provenance.Sources[l.Name] = l.Version
		}
	}
	if in.PlatformTemplate.present() {
		rev.Provenance.Template["platform"] = fmt.Sprintf("%s@%d", in.PlatformTemplate.Name, in.PlatformTemplate.Version)
	}
	if in.OrgTemplate.present() {
		rev.Provenance.Template["organization"] = fmt.Sprintf("%s@%d", in.OrgTemplate.Name, in.OrgTemplate.Version)
	}
	sum, err := yaml.Marshal(rev.Provenance)
	if err != nil {
		return rev, err
	}
	digest := sha256.Sum256(sum)
	rev.ID = "r-" + hex.EncodeToString(digest[:])[:16]
	rev.Provenance.Revision = rev.ID
	rev.Docs = map[string][]byte{}
	for _, name := range Names {
		raw, err := marshalDoc(name, rev.Provenance, bodies[name])
		if err != nil {
			return rev, err
		}
		rev.Docs[name] = raw
	}
	return rev, nil
}

// projectGrants snapshots the trusted ledger into the grants passport: an
// operation the capability does not declare fails closed, and the merged
// ceiling bounds which capability.operation pairs the ledger may admit at
// all — the ledger cannot mint authority outside the ceilings (R5.2).
func projectGrants(ledger map[string][]string, docs catalog.Docs, ceiling any) (map[string]any, error) {
	bound, _ := ceiling.([]any)
	out := map[string]any{}
	for cap, ops := range ledger {
		if !contains(bound, cap) {
			return nil, fmt.Errorf("passport compile: grant %q outside the ceiling", cap)
		}
		row := make([]any, 0, len(ops))
		for _, op := range ops {
			if err := docs.ValidateRight(cap, op); err != nil {
				return nil, fmt.Errorf("passport compile: grant %q: %w", cap, err)
			}
			row = append(row, op)
		}
		out[cap] = row
	}
	return map[string]any{"allow": out}, nil
}

// narrowAgentLeaf checks one agent-layer leaf against the spec the layers
// above produced (ADR-0106: lower layers only narrow). The leaf must
// already exist in the merged result — a list may shrink to a subset (a
// choice list narrows to one member) and a scalar must repeat the value.
// Anything else — a new path, a new keyset entry, a wider list, a changed
// scalar — is a widening and a compile error.
func narrowAgentLeaf(name, doc, p string, raw any, class fieldClass, above map[string]any) error {
	over, present := above[p]
	if !present {
		return fmt.Errorf("passport compile: agent layer %q adds %s.%s beyond the user layer", name, doc, p)
	}
	if list, isList := over.([]any); isList {
		if class == classChoice {
			if contains(list, raw) {
				return nil
			}
		} else if rawList, ok := raw.([]any); ok {
			for _, item := range rawList {
				if !contains(list, item) {
					return fmt.Errorf("passport compile: agent layer %q widens %s.%s beyond the user layer", name, doc, p)
				}
			}
			return nil
		}
		return fmt.Errorf("passport compile: agent layer %q widens %s.%s beyond the user layer", name, doc, p)
	}
	if fmt.Sprint(raw) != fmt.Sprint(over) {
		return fmt.Errorf("passport compile: agent layer %q widens %s.%s beyond the user layer", name, doc, p)
	}
	return nil
}

// contains reports whether item is a member of the offered bound list.
func contains(bound []any, item any) bool {
	for _, b := range bound {
		if fmt.Sprint(b) == fmt.Sprint(item) {
			return true
		}
	}
	return false
}

func setPath(body map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	m := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = value
}

// mapAt navigates a dotted path through body; an absent or non-map node
// yields nil.
func mapAt(body map[string]any, path string) map[string]any {
	m := body
	for _, p := range strings.Split(path, ".") {
		next, ok := m[p].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// meta projects the revision provenance into the short v2 file envelope:
// the platform template is the authoring template (organization fills in
// only when no platform template bound the space) and catalogs collapse
// to the single release they share.
func (p Provenance) meta() Metadata {
	template := p.Template["platform"]
	if template == "" {
		template = p.Template["organization"]
	}
	releases := map[string]bool{}
	for _, r := range p.Catalogs {
		releases[r] = true
	}
	unique := make([]string, 0, len(releases))
	for r := range releases {
		unique = append(unique, r)
	}
	slices.Sort(unique)
	return Metadata{
		Space: p.Space, Environment: p.Environment, Revision: p.Revision,
		GeneratedAt: p.GeneratedAt, Template: template, Catalogs: strings.Join(unique, ","),
	}
}

// document is the v2 file shape: contract keys are top-level via the
// inline body — there is no body: wrapper (ADR-0093 D2).
type document struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   Metadata       `yaml:"metadata"`
	Body       map[string]any `yaml:",inline"`
}

func marshalDoc(name string, p Provenance, body map[string]any) ([]byte, error) {
	return yaml.Marshal(document{APIVersion: APIVersion, Kind: name, Metadata: p.meta(), Body: body})
}

// Value reads one dotted path out of a compiled passport body; an absent
// doc or path yields nil.
func (r Revision) Value(doc, path string) (any, error) {
	var d map[string]any
	if err := yaml.Unmarshal(r.Docs[doc], &d); err != nil {
		return nil, err
	}
	var cur any = d
	for _, part := range strings.Split(path, ".") {
		m, _ := cur.(map[string]any)
		cur = m[part]
	}
	return cur, nil
}

// value is the error-free Value shorthand for generated renderers.
func (r Revision) value(doc, path string) any {
	v, _ := r.Value(doc, path)
	return v
}

// GrantAllow parses the grants passport's effective allow snapshot; a
// revision without the doc yields nil.
func (r Revision) GrantAllow() (map[string][]string, error) {
	var d struct {
		Allow map[string][]string `yaml:"allow"`
	}
	if err := yaml.Unmarshal(r.Docs["grants"], &d); err != nil {
		return nil, err
	}
	return d.Allow, nil
}

// Current returns the active revision id.
