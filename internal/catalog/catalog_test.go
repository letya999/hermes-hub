package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// TestPlatformCatalogParityAndPinnedToolset is the M29.1 contract test: the
// generated catalogs must equal the committed immutable release, stay
// byte-stable, cover every real handler/section/service, and fail closed on
// unknown vocabulary or a foreign release.
func TestPlatformCatalogParityAndPinnedToolset(t *testing.T) {
	docs, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// --- parity: every tool in the reviewed definition lands in a capability
	def := agenttools.HubToolsDefinition()
	if docs.Capabilities.DefinitionID != def.DefinitionID || docs.Capabilities.DefinitionVersion != def.Version {
		t.Fatalf("definition pin drift: %s@%s vs %s@%s", docs.Capabilities.DefinitionID, docs.Capabilities.DefinitionVersion, def.DefinitionID, def.Version)
	}
	for _, spec := range def.Tools {
		cap, err := docs.Capability(spec.CapabilityID)
		if err != nil {
			t.Errorf("tool %s: %v", spec.Name, err)
			continue
		}
		if !slices.Contains(cap.Tools, spec.Name) {
			t.Errorf("capability %s misses member tool %s", spec.CapabilityID, spec.Name)
		}
		for _, use := range spec.Uses {
			if !slices.Contains(cap.Operations, use.Action) {
				t.Errorf("capability %s misses operation %s (tool %s)", spec.CapabilityID, use.Action, spec.Name)
			}
			if !slices.Contains(cap.Resources, use.Resource) {
				t.Errorf("capability %s misses resource %s (tool %s)", spec.CapabilityID, use.Resource, spec.Name)
			}
		}
	}

	// --- parity: every toolset/section/tool surfaces in docs/tools.md
	// Wildcard doc rows (`ssh_shell_*`) cover every tool sharing the prefix —
	// same rule as TestCatalogToolsDocumented.
	docsBody, err := os.ReadFile(filepath.Join("..", "..", "docs", "tools.md"))
	if err != nil {
		t.Fatalf("docs/tools.md: %v", err)
	}
	tokens := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z_]+\\*?)`").FindAllStringSubmatch(string(docsBody), -1) {
		tokens[m[1]] = true
	}
	for id, cap := range docs.Capabilities.Capabilities {
		for _, name := range cap.Tools {
			if tokens[name] {
				continue
			}
			covered := false
			for token := range tokens {
				if strings.HasSuffix(token, "*") && strings.HasPrefix(name, strings.TrimSuffix(token, "*")) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("capability %s member %s missing from docs/tools.md", id, name)
			}
		}
	}

	// --- parity: schedule tools document in docs/routines.md
	routinesBody, err := os.ReadFile(filepath.Join("..", "..", "docs", "routines.md"))
	if err != nil {
		t.Fatalf("docs/routines.md: %v", err)
	}
	for id, cap := range docs.Capabilities.Capabilities {
		family := strings.SplitN(id, ".", 2)[0]
		if family != "routines" && family != "watches" {
			continue
		}
		for _, name := range cap.Tools {
			if !strings.Contains(string(routinesBody), name) {
				t.Errorf("capability %s member %s missing from docs/routines.md", id, name)
			}
		}
	}

	// --- parity: every governance section resolves to a catalog surface.
	// r5 names toolsets logically: hub:<name> sections admit the family or
	// connector toolset <name>; hub:tools is the executor-governance
	// section — it admits the runtime executor itself and owns no
	// placeable surface (the pinned platform family is its model face).
	for _, section := range api.SeedInventory() {
		if _, ok := docs.Capabilities.Sections[section.ID]; !ok {
			t.Errorf("section %s missing from capabilities catalog", section.ID)
		}
		switch {
		case strings.HasPrefix(section.ID, "native:"):
			ts, err := docs.Toolset(section.ID)
			if err != nil || (ts.Kind != "native" && ts.Kind != "pseudo") {
				t.Errorf("native section %s has no native/pseudo toolset: %v", section.ID, err)
			}
		case section.ID == api.SectionHubTools:
		case strings.HasPrefix(section.ID, "hub:"):
			if _, err := docs.Toolset(strings.TrimPrefix(section.ID, "hub:")); err != nil {
				t.Errorf("hub section %s has no toolset: %v", section.ID, err)
			}
		}
	}

	// --- parity: every pinned upstream toolset is cataloged; carve-outs match
	inventory, err := stack.ManagedCapabilityInventory()
	if err != nil {
		t.Fatalf("ManagedCapabilityInventory: %v", err)
	}
	carveouts := map[string]bool{}
	for _, name := range stack.NativeCarveoutToolsets() {
		carveouts[name] = true
	}
	for _, name := range inventory.DisabledToolsets {
		ts, err := docs.Toolset("native:" + name)
		if err != nil {
			t.Errorf("upstream toolset %s missing: %v", name, err)
			continue
		}
		if ts.Carveout != carveouts[name] {
			t.Errorf("toolset %s carveout=%v, want %v", name, ts.Carveout, carveouts[name])
		}
	}
	// Pseudo-toolsets and `tools:` aliases are cataloged as themselves, not
	// as upstream natives: the compiler must not treat them interchangeably.
	if ts, err := docs.Toolset("native:deep_research"); err != nil || ts.Kind != "pseudo" {
		t.Errorf("deep_research: kind %q err %v, want pseudo", ts.Kind, err)
	}
	if ts, err := docs.Toolset("files"); err != nil || !slices.Contains(ts.Aliases, "file") {
		t.Errorf("files toolset aliases %v miss \"file\"", ts.Aliases)
	}
	if ts, err := docs.Toolset("terminal"); err != nil || !slices.Contains(ts.Aliases, "code_exec") {
		t.Errorf("terminal toolset aliases %v miss \"code_exec\"", ts.Aliases)
	}
	if ts, err := docs.Toolset("connectors"); err != nil || ts.Kind != "family" || len(ts.Members) == 0 {
		t.Errorf("connectors family drifted: %+v err %v", ts, err)
	}
	// Same name, different executor: native:terminal (upstream toolset) and
	// terminal (executor family) are distinct entries — never merged.
	if native, fam := docs.Toolsets.Toolsets["native:terminal"], docs.Toolsets.Toolsets["terminal"]; native.Kind != "native" || fam.Kind != "family" {
		t.Errorf("terminal collision merged: native=%q family=%q", native.Kind, fam.Kind)
	}
	// Inventory names the hub never exposes as sections are recorded as
	// denied-by-construction: cataloged, but with no section and no executor.
	if denied, err := docs.Toolset("native:bot_room"); err != nil || denied.Via != nil || denied.Section != "" || denied.Carveout {
		t.Errorf("unplaceable inventory toolset bot_room: %+v err %v", denied, err)
	}

	// --- parity: every feature and every emitted compose service has a kind
	for _, feature := range stack.ServiceCatalog() {
		if _, err := docs.Service(feature.Name); err != nil {
			t.Errorf("feature service %s missing: %v", feature.Name, err)
		}
	}
	for _, env := range []string{"dev", "prod"} {
		s := stack.Settings{Schema: 1, User: "alice", Environment: env, Timezone: "UTC"}
		s.Tools = map[string]stack.ToolEntry{}
		for _, feature := range stack.Features {
			switch feature.Name {
			case "browser_act":
				s.Tools["browser"] = stack.ToolEntry{Via: stack.ToolViaMCP, Tools: map[string]bool{"act": true}}
			case "ssh_write", "ssh_shell", "ssh_tunnel":
				e := s.Tools["ssh"]
				e.Via = stack.ToolViaToolHub
				if e.Tools == nil {
					e.Tools = map[string]bool{}
				}
				e.Tools[strings.TrimPrefix(feature.Name, "ssh_")] = true
				s.Tools["ssh"] = e
			case "google_write":
				s.Tools["google"] = stack.ToolEntry{Via: stack.ToolViaMCP, Tools: map[string]bool{"write": true}}
			case "telegram_write":
				s.Tools["telegram_user"] = stack.ToolEntry{Via: stack.ToolViaMCP, Tools: map[string]bool{"write": true}}
			case "workspace", "telegram", "slack_app", "telegram_auth":
			default:
				s.Tools[feature.Name] = stack.ToolEntry{Via: stack.ToolViaMCP}
			}
		}
		s.Ingress = []string{"telegram", "slack_app"}
		rendered := stack.Compose(s, t.TempDir(), t.TempDir())["services"].(stack.M)
		for name := range rendered {
			kind := normalizeServiceName(name, "alice", env)
			if _, ok := docs.Services.Infra[kind]; !ok {
				t.Errorf("compose service %s (kind %s) missing from services catalog", name, kind)
			}
		}
	}

	// --- determinism: identical sources emit identical bytes
	again, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	first, err := docs.marshalAll()
	if err != nil {
		t.Fatalf("marshalAll: %v", err)
	}
	second, err := again.marshalAll()
	if err != nil {
		t.Fatalf("marshalAll: %v", err)
	}
	for name := range first {
		if !slices.Equal(first[name], second[name]) {
			t.Fatalf("%s is not deterministic", name)
		}
	}

	// --- pinned: the committed release dir must equal generated bytes exactly
	root := filepath.Join("..", "..")
	relDir := filepath.Join(root, "platform", "catalogs", Release)
	for name, want := range first {
		got, err := os.ReadFile(filepath.Join(relDir, name))
		if err != nil {
			t.Fatalf("pinned catalog %s: %v (run devcheck catalog-emit)", name, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("pinned catalog %s drifted: emit a new release, never edit bytes in place", name)
		}
	}
	loaded, err := Load(root, Release)
	if err != nil {
		t.Fatalf("Load pinned release: %v", err)
	}
	if len(loaded.Capabilities.Capabilities) != len(docs.Capabilities.Capabilities) ||
		len(loaded.Toolsets.Toolsets) != len(docs.Toolsets.Toolsets) {
		t.Fatal("loaded release does not match generated vocabulary")
	}

	// --- fail closed: unknown capability/operation/toolset/service/release
	if _, err := docs.Capability("ghost.capability"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown capability admitted: %v", err)
	}
	if err := docs.ValidateOperation("files.list", "list"); err != nil {
		t.Errorf("known operation rejected: %v", err)
	}
	if err := docs.ValidateOperation("files.read", "read"); err != nil {
		t.Errorf("known operation rejected: %v", err)
	}
	if err := docs.ValidateOperation("files.read", "detonate"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown operation admitted: %v", err)
	}
	if err := docs.ValidateOperation("ghost.capability", "list"); !errors.Is(err, ErrUnknown) {
		t.Errorf("operation on unknown capability admitted: %v", err)
	}
	if _, err := docs.Toolset("made-up-toolset"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown toolset admitted: %v", err)
	}
	if _, err := docs.Service("made-up-service"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown service admitted: %v", err)
	}
	if _, err := Load(root, "0"); err == nil {
		t.Error("unsupported catalog release loaded")
	}

	// --- a pinned release is immutable: Emit into an identical dir is a
	// no-op; fail closed on tampered bytes (digest mismatch) and drift.
	tmp := t.TempDir()
	tmpRel := filepath.Join(tmp, "platform", "catalogs", Release)
	if err := os.MkdirAll(tmpRel, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range first {
		if err := os.WriteFile(filepath.Join(tmpRel, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Emit(tmp); err != nil {
		t.Fatalf("Emit into identical pinned dir must be a no-op: %v", err)
	}
	tampered := append(slices.Clone(first[CapabilitiesFile]), []byte("\nextra: true\n")...)
	if err := os.WriteFile(filepath.Join(tmpRel, CapabilitiesFile), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(tmp, Release); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("tampered catalog loaded: %v", err)
	}

	// --- unsupported executors, toolset kinds, dependency and capability
	// shapes are rejected on Validate (maps are shared — regenerate per case)
	fresh := func() Docs {
		d, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		return d
	}
	badVia := fresh()
	badVia.Toolsets.Toolsets["files"] = Toolset{Kind: "family", Via: []string{"teleport"}}
	if err := badVia.Validate(); err == nil {
		t.Error("unsupported executor admitted")
	}
	badCapVia := fresh()
	badEntry := badCapVia.Capabilities.Capabilities["files.read"]
	badEntry.Via = []string{"teleport"}
	badCapVia.Capabilities.Capabilities["files.read"] = badEntry
	if err := badCapVia.Validate(); err == nil {
		t.Error("unsupported capability executor admitted")
	}
	badKind := fresh()
	badKind.Toolsets.Toolsets["files"] = Toolset{Kind: "weird"}
	if err := badKind.Validate(); err == nil {
		t.Error("unknown toolset kind admitted")
	}
	badDep := fresh()
	badDep.Services.Infra["model-relay"] = Service{Kind: "infra", Instances: "per-stack", DependsOn: []string{"ghost"}}
	if err := badDep.Validate(); err == nil {
		t.Error("unknown service dependency admitted")
	}
	badCap := fresh()
	badCap.Capabilities.Capabilities["files.read"] = Capability{Issuer: "platform"}
	if err := badCap.Validate(); err == nil {
		t.Error("empty capability vocabulary admitted")
	}
	if err := docs.Validate(); err != nil {
		t.Errorf("generated catalog failed its own validation: %v", err)
	}

	// --- admitted view: a space sees only the surfaces its governance
	// admitted plus the pinned platform family — never the full catalog
	admitted := docs.Project(map[string]bool{"native:file": true, "hub:files": true})
	if len(admitted.Toolsets.Toolsets) != 3 {
		t.Errorf("admitted view carried %d toolsets, want 3", len(admitted.Toolsets.Toolsets))
	}
	if _, err := admitted.Toolset("native:file"); err != nil {
		t.Errorf("admitted native:file missing: %v", err)
	}
	for name := range admitted.Toolsets.Toolsets {
		if name != "native:file" && name != "files" && name != "platform" {
			t.Errorf("unadmitted toolset %s leaked into the space view", name)
		}
	}
	if _, err := admitted.Capability("files.read"); err != nil {
		t.Errorf("admitted capability files.read missing: %v", err)
	}
	if _, ok := admitted.Capabilities.Sections["hub:files"]; !ok {
		t.Error("admitted section hub:files missing")
	}
	for id := range admitted.Capabilities.Capabilities {
		family := strings.SplitN(id, ".", 2)[0]
		if family != "files" && family != "platform" {
			t.Errorf("unadmitted capability %s leaked into the space view", id)
		}
	}
	for id := range admitted.Capabilities.Sections {
		if id != "native:file" && id != "hub:files" {
			t.Errorf("unadmitted section %s leaked into the space view", id)
		}
	}
	if admitted.Capabilities.Release != docs.Capabilities.Release || admitted.Capabilities.DefinitionVersion != docs.Capabilities.DefinitionVersion {
		t.Error("admitted view lost its release pins")
	}
	// Projecting nothing at all must still yield a valid view holding only
	// the pinned platform family — a space with no grants never widens,
	// and never loses its discovery surface.
	empty := docs.Project(map[string]bool{})
	if len(empty.Toolsets.Toolsets) != 1 {
		t.Errorf("empty admission carried %d toolsets, want pinned platform only", len(empty.Toolsets.Toolsets))
	}
	for id := range empty.Capabilities.Capabilities {
		if strings.SplitN(id, ".", 2)[0] != "platform" {
			t.Errorf("unadmitted capability %s leaked into the empty view", id)
		}
	}
	if len(empty.Capabilities.Sections) != 0 {
		t.Error("empty admission produced sections")
	}
	if err := empty.Validate(); err != nil {
		t.Errorf("empty view invalid: %v", err)
	}

	// --- a pinned toolset never grows: Emit refuses in-place drift
	shrunk := slices.Clone(first[ToolsetsFile])
	shrunk = shrunk[:len(shrunk)-10]
	if err := os.WriteFile(filepath.Join(tmpRel, ToolsetsFile), shrunk, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Emit(tmp); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Errorf("Emit silently rewrote a pinned release: %v", err)
	}
}

// normalizeServiceName derives the infra kind from an emitted compose
// service name: <kind>[-<user>][-<env>] with hosting's -ctl/-init siblings
// folding into the hosting kind.
func normalizeServiceName(name, user, env string) string {
	for _, tail := range []string{"-ctl", "-init"} {
		name = strings.TrimSuffix(name, tail)
	}
	for _, suffix := range []string{"-" + user + "-" + env, "-" + user} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			break
		}
	}
	return name
}
