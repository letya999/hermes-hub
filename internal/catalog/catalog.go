// Package catalog emits the immutable platform catalogs ADR-0092 D3 freezes
// for the six-passport contract: capabilities, toolsets and services. Every
// record is generated from the same Go sources the runtime enforces —
// HubToolsDefinition, SeedInventory, the pinned upstream capability
// inventory, ServiceCatalog and the compose service kinds — never from a
// second hand-written authority. Emitted bytes under
// platform/catalogs/<release>/ are immutable: Emit refuses to overwrite
// drifted files, so a vocabulary change must bump Release and land as a new
// catalog directory.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const Release = "37"

const (
	CapabilitiesFile = "capabilities.yaml"
	ToolsetsFile     = "toolsets.yaml"
	ServicesFile     = "services.yaml"
	ManifestFile     = "manifest.yaml"
)

var ErrUnknown = errors.New("unknown catalog entry")

// PlatformCatalogRoot is where hub images bake the immutable catalog tree
// (`COPY platform/catalogs /opt/hub/platform/catalogs` in the core stage —
// root-owned read-only files the agent uid can never edit). HUB_CATALOG_ROOT
// overrides resolution for host checkouts and dev mounts.
const PlatformCatalogRoot = "/opt/hub"

// Root resolves the directory holding platform/catalogs: the
// HUB_CATALOG_ROOT override, then the baked image path, then an ancestor
// walk for a host checkout that already pins this release.
func Root() string {
	if dir := os.Getenv("HUB_CATALOG_ROOT"); dir != "" {
		return dir
	}
	catalogs := filepath.Join("platform", "catalogs")
	if _, err := os.Stat(filepath.Join(PlatformCatalogRoot, catalogs, Release, ManifestFile)); err == nil {
		return PlatformCatalogRoot
	}
	for dir, err := os.Getwd(); err == nil; {
		if _, err := os.Stat(filepath.Join(dir, catalogs, Release, ManifestFile)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return PlatformCatalogRoot
}

// LoadPinned loads the active release from the resolved Root: the manifest
// digests must verify before any record is trusted.
func LoadPinned() (Docs, error) {
	return Load(Root(), Release)
}

type Capability struct {
	Operations []string `yaml:"operations"`
	Resources  []string `yaml:"resources"`
	Effect     string   `yaml:"effect"`
	Scope      string   `yaml:"scope"`
	Issuer     string   `yaml:"issuer"`
	Via        []string `yaml:"via"`
	Tools      []string `yaml:"tools"`
}

type Section struct {
	Owner  string `yaml:"owner"`
	Effect string `yaml:"effect"`
}

type CapabilitiesDoc struct {
	Release           string                `yaml:"release"`
	DefinitionID      string                `yaml:"definition_id"`
	DefinitionVersion string                `yaml:"definition_version"`
	ControlOperations []string              `yaml:"control_operations"`
	Capabilities      map[string]Capability `yaml:"capabilities"`
	Sections          map[string]Section    `yaml:"sections"`
}

type Toolset struct {
	Kind      string              `yaml:"kind"`
	Effect    string              `yaml:"effect"`
	Via       []string            `yaml:"via,omitempty"`
	Executor  api.Executor        `yaml:"executor,omitempty"`
	Carveout  bool                `yaml:"carveout,omitempty"`
	Pinned    bool                `yaml:"pinned,omitempty"`
	Uncurated bool                `yaml:"uncurated,omitempty"`
	Section   string              `yaml:"section,omitempty"`
	Aliases   []string            `yaml:"aliases,omitempty"`
	Members   []string            `yaml:"members,omitempty"`
	Actions   []string            `yaml:"actions,omitempty"`
	Requires  map[string][]string `yaml:"requires,omitempty"`
}

type ToolsetsDoc struct {
	Release  string             `yaml:"release"`
	Toolsets map[string]Toolset `yaml:"toolsets"`
}

type Service struct {
	Kind        string   `yaml:"kind"`
	Instances   string   `yaml:"instances,omitempty"`
	Requires    []string `yaml:"requires,omitempty"`
	DependsOn   []string `yaml:"depends_on,omitempty"`
	SelfService bool     `yaml:"self_service,omitempty"`
}

type ServicesDoc struct {
	Release  string             `yaml:"release"`
	Features map[string]Service `yaml:"features"`
	Infra    map[string]Service `yaml:"infra"`
}

type Docs struct {
	Capabilities CapabilitiesDoc
	Toolsets     ToolsetsDoc
	Services     ServicesDoc
	Rights       RightsDoc
}

// infraServices is the reviewed registry of platform-owned service kinds.
// Instance names in a space compose carry the owner suffix
// (<kind>-<user>[-<env>]); the parity test derives these kinds back from
// Compose, so a service added without a catalog entry fails the suite.
var infraServices = map[string]Service{
	"hermes-runtime":      {Kind: "infra", Instances: "per-runtime", DependsOn: []string{"toolhub", "model-relay", "communication-hub"}},
	"supervisor":          {Kind: "infra", Instances: "per-stack", DependsOn: []string{"credential-broker"}},
	"toolhub":             {Kind: "infra", Instances: "per-stack", DependsOn: []string{"credential-broker", "model-relay"}},
	"toolhub-relay":       {Kind: "infra", Instances: "per-stack"},
	"model-relay":         {Kind: "infra", Instances: "per-stack", DependsOn: []string{"cliproxy"}},
	"egress-relay":        {Kind: "infra", Instances: "per-stack"},
	"cliproxy":            {Kind: "infra", Instances: "shared|dedicated"},
	"communication-hub":   {Kind: "infra", Instances: "per-stack", DependsOn: []string{"credential-broker", "supervisor"}},
	"credential-broker":   {Kind: "infra", Instances: "per-stack"},
	"workload-controller": {Kind: "infra", Instances: "per-stack"},
	"hub-media":           {Kind: "infra", Instances: "per-stack", DependsOn: []string{"credential-broker"}},
	"hub-stt":             {Kind: "infra", Instances: "per-stack"},
	"hub-tts":             {Kind: "infra", Instances: "per-stack"},
	"browser":             {Kind: "infra", Instances: "per-space"},
	"hosting":             {Kind: "infra", Instances: "per-space-env", DependsOn: []string{"workload-controller"}},
	"open-knowledge":      {Kind: "infra", Instances: "per-space"},
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	slices.Sort(out)
	return out
}

func mergeUnique(dst []string, values ...string) []string {
	for _, v := range values {
		if !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}

// generateCapabilities derives every executor capability from the reviewed
// tool definition plus the control-plane operation and governance-section
// vocabulary.
func generateCapabilities() CapabilitiesDoc {
	def := agenttools.HubToolsDefinition()
	caps := map[string]Capability{}
	for _, spec := range def.Tools {
		cap := caps[spec.CapabilityID]
		cap.Tools = append(cap.Tools, spec.Name)
		if spec.Effect == api.WriteEffect {
			cap.Effect = "write"
		}
		for _, use := range spec.Uses {
			cap.Operations = mergeUnique(cap.Operations, use.Action)
			cap.Resources = mergeUnique(cap.Resources, use.Resource)
			if use.PathArgument != "" {
				cap.Scope = "path"
			}
		}
		caps[spec.CapabilityID] = cap
	}
	for id, cap := range caps {
		if cap.Effect == "" {
			cap.Effect = "read"
		}
		if cap.Scope == "" {
			cap.Scope = "call"
		}
		// Remote-executor capabilities (runtime:, connector:, flow:) carry
		// no in-runtime uses: the family.id spelling is the honest op —
		// <family>.<action> over resource <family>.
		if len(cap.Operations) == 0 {
			family, action, _ := strings.Cut(id, ".")
			cap.Operations = []string{action}
			cap.Resources = []string{family}
		}
		cap.Issuer = "platform"
		cap.Via = []string{"toolhub"}
		slices.Sort(cap.Tools)
		slices.Sort(cap.Operations)
		slices.Sort(cap.Resources)
		caps[id] = cap
	}
	sections := map[string]Section{}
	for _, s := range api.SeedInventory() {
		sections[s.ID] = Section{Owner: s.Owner, Effect: s.Effect}
	}
	return CapabilitiesDoc{
		Release:           Release,
		DefinitionID:      def.DefinitionID,
		DefinitionVersion: def.Version,
		ControlOperations: sorted(api.ControlOperations),
		Capabilities:      caps,
		Sections:          sections,
	}
}

// generateToolsets joins the pinned upstream inventory, the pseudo-toolsets,
// the executor capability families and the hub connector surfaces into one
// placeable vocabulary. Keys are section-style — `native:<name>` for the
// upstream/pseudo surface and `hub:<family>` for the executor — so the same
// word (terminal, skills, web, image_gen) can name two distinct placeable
// surfaces without ambiguity.
func generateToolsets(caps CapabilitiesDoc) (map[string]Toolset, error) {
	inventory, err := stack.ManagedCapabilityInventory()
	if err != nil {
		return nil, err
	}
	out := map[string]Toolset{}
	carveouts := map[string]bool{}
	for _, name := range stack.NativeCarveoutToolsets() {
		carveouts[name] = true
	}
	pseudo := map[string]bool{}
	for _, name := range stack.PseudoToolsetNames() {
		pseudo[name] = true
	}
	// Native vocabulary: the upstream inventory, the pseudo-toolsets and the
	// native:* governance sections. Inventory names with no section (bot_room,
	// delegation, ...) are recorded as denied-by-construction: cataloged, but
	// with no governance section and no admitted executor.
	nativeNames := map[string]bool{}
	for _, name := range inventory.DisabledToolsets {
		nativeNames[name] = true
	}
	for name := range pseudo {
		nativeNames[name] = true
	}
	for id := range caps.Sections {
		if strings.HasPrefix(id, "native:") {
			nativeNames[strings.TrimPrefix(id, "native:")] = true
		}
	}
	for name := range nativeNames {
		key := "native:" + name
		ts := Toolset{Kind: "native", Carveout: carveouts[name]}
		if pseudo[name] {
			ts.Kind = "pseudo"
		}
		// Only the reviewed carveouts and pseudo-toolsets are placeable;
		// every other inventory name stays denied-by-design — cataloged
		// with its reason, but no governance section and no route.
		if sec, ok := caps.Sections[key]; ok && (carveouts[name] || pseudo[name]) {
			ts.Section = key
			ts.Effect = sec.Effect
			ts.Via = []string{"native"}
			ts.Executor = api.ExecutorNative
		}
		out[key] = ts
	}
	// First-party families (catalog r5, ADR-0094 D1): placeable under their
	// logical name, executor taken from the member tool specs. Members are
	// model-audience tools only — a family with no model members (inbox)
	// never mints a placeable surface.
	specByName := map[string]api.ToolSpec{}
	for _, spec := range agenttools.HubToolsDefinition().Tools {
		specByName[spec.Name] = spec
	}
	families := map[string]bool{}
	aliases := map[string][]string{}
	for id := range caps.Capabilities {
		families[strings.SplitN(id, ".", 2)[0]] = true
	}
	for name, id := range stack.ToolHubEntryCapabilities() {
		families[id] = true
		if name != id && !slices.Contains(aliases[id], name) {
			aliases[id] = append(aliases[id], name)
		}
	}
	members := map[string][]string{}
	for id, cap := range caps.Capabilities {
		family := strings.SplitN(id, ".", 2)[0]
		for _, member := range cap.Tools {
			if spec, ok := specByName[member]; !ok || spec.Audience != api.AudienceModel {
				continue
			}
			members[family] = append(members[family], member)
		}
	}
	for family := range families {
		names := sorted(members[family])
		if len(names) == 0 {
			continue
		}
		executor := specByName[names[0]].Executor
		if family == "platform" {
			out[family] = Toolset{Kind: "family", Effect: "mutate", Via: []string{"toolhub"}, Executor: executor, Pinned: true, Members: names}
			continue
		}
		section := "hub:" + family
		effect, known := "", false
		if sec, ok := caps.Sections[section]; ok {
			effect, known = sec.Effect, true
		}
		if !known {
			section = api.SectionHubTools
			effect = "read"
			for id, cap := range caps.Capabilities {
				if strings.SplitN(id, ".", 2)[0] == family && cap.Effect == "write" {
					effect = "mutate"
					break
				}
			}
		}
		out[family] = Toolset{Kind: "family", Effect: effect, Via: []string{"toolhub"}, Executor: executor, Section: section, Aliases: sorted(aliases[family]), Members: names}
	}
	// Connector toolsets: curated MCP surfaces (github, google, ...) named
	// after their governance section minus the hub: prefix, members
	// projected dynamically by the connector owner — never listed by the
	// platform. A name already claimed by a family (browser) is served by
	// the family's curated tools instead.
	for id, sec := range caps.Sections {
		if !strings.HasPrefix(id, "hub:") || id == api.SectionHubTools {
			continue
		}
		name := strings.TrimPrefix(id, "hub:")
		if _, isFamily := out[name]; isFamily {
			continue
		}
		out[name] = Toolset{Kind: "connector", Effect: sec.Effect, Via: []string{"mcp"}, Executor: "connector:" + name, Section: id}
	}
	// Raw MCP stays default-denied (R10.1): the only placeable raw surfaces
	// are the reviewed user/org catalogs — uncurated, members projected
	// dynamically by the declared servers, never in core. An ad-hoc
	// mcp-raw:<server> name is never a toolset — it cannot mint its own
	// section.
	out[api.SectionUserMCP] = Toolset{Kind: "connector", Effect: "mutate", Via: []string{"mcp-raw"}, Executor: api.ExecutorNative, Uncurated: true, Section: api.SectionUserMCP}
	out[api.SectionOrgMCP] = Toolset{Kind: "connector", Effect: "mutate", Via: []string{"mcp-raw"}, Executor: api.ExecutorNative, Uncurated: true, Section: api.SectionOrgMCP}
	if err := annotateToolsets(out, caps); err != nil {
		return nil, err
	}
	return out, nil
}

// generateServices joins the feature service catalog with the reviewed
// platform infra kinds.
func generateServices() ServicesDoc {
	features := map[string]Service{}
	for _, f := range stack.ServiceCatalog() {
		features[f.Name] = Service{Kind: "feature", Requires: sorted(f.Requires), DependsOn: sorted(f.Depends), SelfService: f.SelfService}
	}
	infra := map[string]Service{}
	for name, svc := range infraServices {
		infra[name] = svc
	}
	return ServicesDoc{Release: Release, Features: features, Infra: infra}
}

// Generate builds the three catalogs deterministically: sorted collections,
// no timestamps — identical sources always produce identical bytes.
func Generate() (Docs, error) {
	caps := generateCapabilities()
	toolsets, err := generateToolsets(caps)
	if err != nil {
		return Docs{}, err
	}
	return Docs{Capabilities: caps, Toolsets: ToolsetsDoc{Release: Release, Toolsets: toolsets}, Services: generateServices(), Rights: generateRights()}, nil
}

const generatedHeader = "# Generated by devcheck catalog-emit — immutable under this release. Do not edit by hand.\n"

// Manifest binds a release directory: the file digests Load verifies before
// any record is trusted.
type Manifest struct {
	Release string            `yaml:"release"`
	Files   map[string]string `yaml:"files"`
}

var catalogFiles = []string{CapabilitiesFile, ToolsetsFile, ServicesFile, RightsFile}

func marshalDoc(doc any) ([]byte, error) {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(generatedHeader), body...), nil
}

// marshalAll renders the three catalogs plus their digest manifest.
func (d Docs) marshalAll() (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, doc := range map[string]any{CapabilitiesFile: d.Capabilities, ToolsetsFile: d.Toolsets, ServicesFile: d.Services, RightsFile: d.Rights} {
		body, err := marshalDoc(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = body
	}
	manifest := Manifest{Release: Release, Files: map[string]string{}}
	for _, name := range catalogFiles {
		sum := sha256.Sum256(out[name])
		manifest.Files[name] = hex.EncodeToString(sum[:])
	}
	body, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	out[ManifestFile] = append([]byte(generatedHeader), body...)
	return out, nil
}

// Emit writes platform/catalogs/<Release>/ under root.
func Emit(root string) error {
	docs, err := Generate()
	if err != nil {
		return err
	}
	rendered, err := docs.marshalAll()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "platform", "catalogs", Release)
	for name, body := range rendered {
		path := filepath.Join(dir, name)
		existing, err := os.ReadFile(path) // #nosec G304 -- generated path under root
		if err == nil {
			if !slices.Equal(existing, body) {
				return fmt.Errorf("immutable catalog drift: %s differs from generated vocabulary; bump catalog.Release", path)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a pinned release directory: the manifest digests must cover
// every catalog file before any record is trusted — anything short fails
// closed.
func Load(root, release string) (Docs, error) {
	dir := filepath.Join(root, "platform", "catalogs", release)
	var manifest Manifest
	body, err := os.ReadFile(filepath.Join(dir, ManifestFile)) // #nosec G304 -- pinned release dir
	if err != nil {
		return Docs{}, err
	}
	if err := yaml.Unmarshal(body, &manifest); err != nil {
		return Docs{}, err
	}
	docs := Docs{}
	targets := map[string]any{CapabilitiesFile: &docs.Capabilities, ToolsetsFile: &docs.Toolsets, ServicesFile: &docs.Services, RightsFile: &docs.Rights}
	for name, target := range targets {
		want, ok := manifest.Files[name]
		if !ok {
			return Docs{}, fmt.Errorf("catalog manifest %q misses %s", release, name)
		}
		body, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- pinned release dir
		if err != nil {
			return Docs{}, err
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			return Docs{}, fmt.Errorf("catalog %s digest mismatch in release %q", name, release)
		}
		if err := yaml.Unmarshal(body, target); err != nil {
			return Docs{}, err
		}
	}
	return docs, nil
}
func (d Docs) Capability(id string) (Capability, error) {
	cap, ok := d.Capabilities.Capabilities[id]
	if !ok {
		return Capability{}, fmt.Errorf("%w: capability %q", ErrUnknown, id)
	}
	return cap, nil
}

// ValidateOperation admits an operation only inside its capability's
// cataloged vocabulary.
func (d Docs) ValidateOperation(capability, operation string) error {
	cap, err := d.Capability(capability)
	if err != nil {
		return err
	}
	if !slices.Contains(cap.Operations, operation) {
		return fmt.Errorf("%w: operation %q for capability %q", ErrUnknown, operation, capability)
	}
	return nil
}

// Right resolves an RBAC domain by name.
func (d Docs) Right(name string) (RightCapability, error) {
	cap, ok := d.Rights.Capabilities[name]
	if !ok {
		return RightCapability{}, fmt.Errorf("%w: right capability %q", ErrUnknown, name)
	}
	return cap, nil
}

// RightNames lists the RBAC domain ids in stable order.
func (d Docs) RightNames() []string {
	out := make([]string, 0, len(d.Rights.Capabilities))
	for name := range d.Rights.Capabilities {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// ValidateRight admits an operation only inside its domain's cataloged
// vocabulary and only for the subject class the operation declares.
func (d Docs) ValidateRight(capability, operation string) error {
	cap, err := d.Right(capability)
	if err != nil {
		return err
	}
	if _, ok := cap.Operations[operation]; !ok {
		return fmt.Errorf("%w: operation %q for right capability %q", ErrUnknown, operation, capability)
	}
	return nil
}

func (d Docs) Toolset(name string) (Toolset, error) {
	ts, ok := d.Toolsets.Toolsets[name]
	if !ok {
		return Toolset{}, fmt.Errorf("%w: toolset %q", ErrUnknown, name)
	}
	return ts, nil
}

// Service resolves either a feature name or an infra kind.
func (d Docs) Service(name string) (Service, error) {
	if svc, ok := d.Services.Features[name]; ok {
		return svc, nil
	}
	if svc, ok := d.Services.Infra[name]; ok {
		return svc, nil
	}
	return Service{}, fmt.Errorf("%w: service %q", ErrUnknown, name)
}

var validVia = map[string]bool{"native": true, "toolhub": true, "mcp": true, "mcp-raw": true}

// Project returns the space-scoped admitted view: only the governance
// sections the space is allowed to see, the toolsets behind those sections
// and the capabilities of the admitted executor families. The release pins
// travel with the view so a space can never widen a pinned contract by
// reloading it.
func (d Docs) Project(admittedSections map[string]bool) Docs {
	out := Docs{
		Capabilities: CapabilitiesDoc{
			Release:           d.Capabilities.Release,
			DefinitionID:      d.Capabilities.DefinitionID,
			DefinitionVersion: d.Capabilities.DefinitionVersion,
			Capabilities:      map[string]Capability{},
			Sections:          map[string]Section{},
		},
		Toolsets: ToolsetsDoc{Release: d.Toolsets.Release, Toolsets: map[string]Toolset{}},
		Services: ServicesDoc{Release: d.Services.Release, Features: map[string]Service{}, Infra: map[string]Service{}},
		Rights:   d.Rights,
	}
	admittedFamilies := map[string]bool{}
	for name, ts := range d.Toolsets.Toolsets {
		if ts.Pinned {
			out.Toolsets.Toolsets[name] = ts
			if ts.Kind == "family" {
				admittedFamilies[name] = true
			}
			continue
		}
		if ts.Section == "" || !admittedSections[ts.Section] {
			continue
		}
		out.Toolsets.Toolsets[name] = ts
		if ts.Kind == "family" {
			admittedFamilies[name] = true
		}
	}
	for id, sec := range d.Capabilities.Sections {
		if admittedSections[id] {
			out.Capabilities.Sections[id] = sec
		}
	}
	for id, cap := range d.Capabilities.Capabilities {
		if admittedFamilies[strings.SplitN(id, ".", 2)[0]] {
			out.Capabilities.Capabilities[id] = cap
		}
	}
	return out
}

// Validate rejects a record whose executor, toolset kind or service
// dependency is outside the cataloged vocabulary, or a capability carrying
// no operations, resources or member tools.
func (d Docs) Validate() error {
	for id, cap := range d.Capabilities.Capabilities {
		if len(cap.Operations) == 0 || len(cap.Resources) == 0 || len(cap.Tools) == 0 {
			return fmt.Errorf("capability %q: empty vocabulary", id)
		}
		for _, via := range cap.Via {
			if !validVia[via] {
				return fmt.Errorf("capability %q: unknown executor %q", id, via)
			}
		}
	}
	for name, ts := range d.Toolsets.Toolsets {
		switch ts.Kind {
		case "family", "connector", "native", "pseudo":
		default:
			return fmt.Errorf("toolset %q: unknown kind %q", name, ts.Kind)
		}
		for _, via := range ts.Via {
			if !validVia[via] {
				return fmt.Errorf("toolset %q: unknown executor %q", name, via)
			}
		}
	}
	for name, svc := range d.Services.Infra {
		for _, dep := range svc.DependsOn {
			if _, ok := d.Services.Infra[dep]; !ok {
				return fmt.Errorf("infra service %q: unknown dependency %q", name, dep)
			}
		}
	}
	validSubject := map[string]bool{"self": true, "cross": true}
	validScope := map[string]bool{"none": true, "path": true, "model": true, "toolset": true, "skill": true, "connector": true, "service": true, "channel": true, "log": true, "session": true, "url": true, "host": true}
	validIssuer := map[string]bool{"platform": true, "org": true, "operator": true}
	validEffect := map[string]bool{"read": true, "write": true, "control": true}
	for name, cap := range d.Rights.Capabilities {
		if len(cap.Operations) == 0 || cap.Description == "" {
			return fmt.Errorf("right capability %q: needs a description and at least one operation", name)
		}
		for op, spec := range cap.Operations {
			if spec.Description == "" {
				return fmt.Errorf("right %s.%s: description required", name, op)
			}
			if !validSubject[spec.Subject] {
				return fmt.Errorf("right %s.%s: unknown subject %q", name, op, spec.Subject)
			}
			if !validScope[spec.Scope] {
				return fmt.Errorf("right %s.%s: unknown scope %q", name, op, spec.Scope)
			}
			if !validEffect[spec.Effect] {
				return fmt.Errorf("right %s.%s: unknown effect %q", name, op, spec.Effect)
			}
			for _, issuer := range spec.Issuers {
				if !validIssuer[issuer] {
					return fmt.Errorf("right %s.%s: unknown issuer %q", name, op, issuer)
				}
			}
			if len(spec.PEP) == 0 {
				return fmt.Errorf("right %s.%s: at least one enforcement point required", name, op)
			}
		}
	}
	return nil
}
