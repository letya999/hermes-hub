package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

// PassportOptions drives `hubctl space-migrate`: read the legacy editable
// authority of one space, derive the six-passport source layers, compile the
// revision and — under --apply — publish it with a verified backup. Nothing
// is ever deleted; unknown or dynamic files are preserved untouched.
type PassportOptions struct {
	Dir         string // spaces/<user>
	Environment string // dev|prod, default prod
	Apply       bool
	Catalog     catalog.Docs // zero value -> catalog.Generate()
	Now         time.Time
}

// PassportReport is the dry-run/apply output: field-level mapping, conflicts
// that need an operator decision, written files, the verified backup and the
// rollback fence.
type PassportReport struct {
	Mode      string   `json:"mode"`
	Space     string   `json:"space"`
	Revision  string   `json:"revision,omitempty"`
	Mapped    []string `json:"mapped"`
	Conflicts []string `json:"conflicts,omitempty"`
	Files     []string `json:"files,omitempty"`
	Backup    []Object `json:"backup,omitempty"`
	Rollback  string   `json:"rollback"`
}

// MigratePassport converts one space's editable authority into the layered
// six-passport sources (R11.1). Dry-run never writes; apply writes source
// layers under passport-src/, a verified backup under
// generated/migration-backup/<revision>/ and publishes the revision
// atomically. Legacy files stay in place — consumers retire them only after
// they migrate, never mid-flight.
func MigratePassport(opts PassportOptions) (PassportReport, error) {
	var report PassportReport
	if opts.Dir == "" {
		return report, errors.New("space-migrate requires --dir spaces/<user>")
	}
	if opts.Environment == "" {
		opts.Environment = "prod"
	}
	if opts.Environment != "dev" && opts.Environment != "prod" {
		return report, errors.New("environment must be dev or prod")
	}
	report.Mode = "dry-run"
	if opts.Apply {
		report.Mode = "apply"
	}
	s, err := stack.Read(opts.Dir)
	if err != nil && errors.Is(err, stack.ErrRevisionUnloadable) {
		// A stale/unloadable published revision (e.g. a v1 envelope from the
		// previous compiler) must not block re-migration: the editable legacy
		// files are the intent to transform — read them without the overlay.
		s, err = stack.ReadBase(opts.Dir)
	}
	if err != nil {
		return report, fmt.Errorf("space-migrate: %w", err)
	}
	// Topology derives from the intent view — the owner's files plus
	// operator profiles — never from the previously published revision: a
	// stale passport's modes must not mask dedicated placements before the
	// services map is re-derived.
	if intent, ierr := stack.ReadBase(opts.Dir); ierr == nil {
		s.PassportServices = nil
		s.Cliproxy = intent.Cliproxy
		s.CliproxyCredentials = intent.CliproxyCredentials
	}
	report.Space = s.User
	// Mixed old/new authority is a conflict, never a silent merge: the
	// operator must know settings.yaml is still being read alongside
	// agent/workspace split files before any layer is derived.
	if _, err := os.Stat(filepath.Join(opts.Dir, "settings.yaml")); err == nil {
		for _, name := range []string{"agent.yaml", "workspace.yaml"} {
			if _, err := os.Stat(filepath.Join(opts.Dir, name)); err == nil {
				report.Conflicts = append(report.Conflicts, "mixed legacy settings.yaml and "+name+": both authorities present — review precedence before retiring either")
			}
		}
	}
	docs := opts.Catalog
	if docs.Capabilities.Release == "" {
		if docs, err = catalog.Generate(); err != nil {
			return report, err
		}
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	layers, mapped, conflicts, err := passportLayers(opts.Dir, s, opts.Environment, docs)
	if err != nil {
		return report, err
	}
	report.Mapped = mapped
	report.Conflicts = append(report.Conflicts, conflicts...)
	ledger, grantConflicts, err := grantLedger(opts.Dir, docs, layers)
	if err != nil {
		return report, err
	}
	report.Conflicts = append(report.Conflicts, grantConflicts...)
	rev, err := passport.Compile(passport.Inputs{
		Space: s.User, Environment: opts.Environment, Now: now,
		PlatformCeiling:  layers[0],
		PlatformTemplate: layers[1],
		OrgCeiling:       layers[2],
		OrgTemplate:      layers[3],
		User:             layers[4],
		Grants:           ledger,
		Catalog:          docs,
		RendersInfra:     s.RendersInfra(),
		ReservedPorts:    stack.ReservedSiblingPorts(opts.Dir),
	})
	if err != nil {
		return report, fmt.Errorf("space-migrate: compile failed, nothing changed: %w", err)
	}
	report.Revision = rev.ID
	if !opts.Apply {
		report.Files = []string{
			filepath.Join(opts.Dir, "passport-src", "user.yaml"),
			filepath.Join(opts.Dir, "passport-src", "platform-ceiling.yaml"),
			filepath.Join(opts.Dir, "passport-src", "platform-template.yaml"),
			filepath.Join(opts.Dir, "generated", "passports", rev.ID) + string(filepath.Separator),
		}
		report.Rollback = "dry-run made no changes"
		return report, nil
	}
	backup, err := backupSources(opts.Dir, rev.ID)
	if err != nil {
		return report, fmt.Errorf("space-migrate: backup failed, nothing changed: %w", err)
	}
	report.Backup = backup
	srcDir := filepath.Join(opts.Dir, "passport-src")
	if err := os.MkdirAll(srcDir, 0700); err != nil {
		return report, err
	}
	for i, name := range []string{"platform-ceiling.yaml", "platform-template.yaml", "org-ceiling.yaml", "org-template.yaml", "user.yaml"} {
		if len(layers[i].Docs) == 0 {
			continue
		}
		b, err := yaml.Marshal(layers[i])
		if err != nil {
			return report, err
		}
		if err := writeFileAtomic(filepath.Join(srcDir, name), b, 0600); err != nil {
			return report, err
		}
	}
	if err := rev.Publish(opts.Dir); err != nil {
		return report, err
	}
	if err := DeriveSpaceRights(opts.Dir, s.User, opts.Environment, rev, docs, now); err != nil {
		return report, err
	}
	report.Files = []string{
		filepath.Join(srcDir, "platform-ceiling.yaml"),
		filepath.Join(srcDir, "platform-template.yaml"),
		filepath.Join(srcDir, "user.yaml"),
		filepath.Join(opts.Dir, "generated", "passports", rev.ID),
		filepath.Join(opts.Dir, "generated", "migration-backup", rev.ID),
	}
	report.Rollback = fmt.Sprintf("restore %s over the space, then remove generated/passports/%s and generated/passports/current",
		filepath.Join(opts.Dir, "generated", "migration-backup", rev.ID), rev.ID)
	return report, nil
}

// passportLayers derives the five source layers from the merged legacy
// settings view. The generated platform ceiling bounds exactly the migrated
// vocabulary — wider bounds belong to the real platform/org sources the
// operator authors at adoption, never to a migration tool.
func passportLayers(dir string, s stack.Settings, env string, docs catalog.Docs) ([]passport.Layer, []string, []string, error) {
	var mapped, conflicts []string
	// tools.* -> tools.core.<toolset>: each enabled entry projects to the
	// governance toolset its section names; the routed executor comes from
	// the catalog so the spec never claims a surface it does not have.
	writable := []string{"/workspace"}
	readOnly := []string{"/config", "/skills"}
	toolsets := map[string]any{}
	for name, e := range s.Tools {
		if e.Via == "" || e.Via == stack.ToolViaOff {
			continue
		}
		sec := s.SectionForEntry(name, e)
		if sec == "" {
			conflicts = append(conflicts, fmt.Sprintf("tools.%s: via %q has no governance section", name, e.Via))
			continue
		}
		display := toolsetDisplayName(docs, sec)
		ts, err := docs.Toolset(display)
		if err != nil || len(ts.Via) == 0 {
			conflicts = append(conflicts, fmt.Sprintf("tools.%s: section %q has no v2 toolset — preserved in toolhub store", name, sec))
			continue
		}
		spec := map[string]any{"via": ts.Via[0]}
		if display == "files" {
			// The normative files contract: read scope covers every reachable
			// path, file_write is bound to the writable subset.
			spec["scope"] = map[string]any{"paths": toAnyStrings(append(slices.Clone(readOnly), writable...))}
			spec["rules"] = map[string]any{"file_write": map[string]any{
				"scope": map[string]any{"paths": toAnyStrings(writable)},
			}}
		}
		if _, dup := toolsets[display]; !dup {
			toolsets[display] = spec
			mapped = append(mapped, fmt.Sprintf("tools.%s -> tools.core.%s", name, display))
		}
	}
	rawServers := []string{}
	for name := range s.MCP {
		rawServers = append(rawServers, name)
	}
	sort.Strings(rawServers)
	if len(rawServers) > 0 {
		sec := "user_mcp"
		if s.Organization != "" {
			sec = "org_mcp"
		}
		if ts, err := docs.Toolset(sec); err == nil && len(ts.Via) > 0 {
			if _, dup := toolsets[sec]; !dup {
				toolsets[sec] = map[string]any{"via": ts.Via[0]}
			}
		}
		mapped = append(mapped, fmt.Sprintf("mcp_servers -> tools.core.%s", sec))
	}
	services, err := derivedServices(dir, s, docs)
	if err != nil {
		return nil, nil, nil, err
	}
	features := s.FeatureList()
	for _, m := range s.Workspace.Mounts {
		if m.Mode == "ro" {
			readOnly = append(readOnly, m.To)
		} else {
			writable = append(writable, m.To)
		}
	}
	svcSpecs, modelSvcs, routes, def, err := derivedModels(dir, s, docs)
	if err != nil {
		return nil, nil, nil, err
	}
	// The legacy model: key is the default-route selector — it maps only
	// when it names a route the registry actually migrated; anything else
	// is reported rather than minting a phantom default (R8.1).
	if s.Model != "" {
		if _, ok := routes[s.Model]; ok {
			def = s.Model
			mapped = append(mapped, "model -> models.default")
		} else {
			conflicts = append(conflicts, fmt.Sprintf("model: %q is not a migrated route — no default emitted", s.Model))
		}
	}
	if s.ModelURL != "" {
		if s.ModelURL == "http://model-relay:8318/v1" {
			mapped = append(mapped, "model_url -> model-relay service endpoint")
		} else {
			conflicts = append(conflicts, fmt.Sprintf("model_url %q: no v2 leaf — relay addressing is topology-derived", s.ModelURL))
		}
	}
	svcMap := map[string]any{}
	for instance, mode := range services {
		svcMap[instance] = map[string]any{"mode": mode}
	}
	for name := range svcSpecs {
		if _, ok := svcMap[name]; !ok {
			svcMap[name] = map[string]any{"mode": "per-space"}
		}
	}
	memoryBackend := "local"
	if s.Honcho {
		memoryBackend = "honcho"
	}
	layout := []any{
		map[string]any{"path": "/workspace/inbox/people", "placeholder": "<channel>/<date>/<attachment-id>"},
		map[string]any{"path": "/workspace/inbox/services", "placeholder": "<service>/<date>/<attachment-id>"},
		"/workspace/work", "/workspace/projects",
		"/workspace/artifacts/documents", "/workspace/artifacts/images",
		"/workspace/artifacts/videos", "/workspace/artifacts/archives",
		"/workspace/artifacts/services", "/workspace/outbox"}

	// ports: host-bound listeners the legacy agent file declared. Each
	// source key lands in mapped or conflicts — a port the migrated stack
	// cannot own is reported dropped, never silently erased (R8.1).
	ports := map[string]any{}
	if s.OAuthPort > 0 {
		ports["oauth-callback"] = s.OAuthPort
		mapped = append(mapped, "oauth_port -> config.ports.oauth-callback")
	}
	// A browser port without the browser sidecar is dead weight — the
	// owning service must exist before the passport publishes its port
	// (SPEC-0092 R4.1).
	if s.BrowserPort > 0 {
		if _, ok := svcMap["browser"]; ok {
			ports["browser-vnc"] = s.BrowserPort
			mapped = append(mapped, "browser_port -> config.ports.browser-vnc")
		} else {
			conflicts = append(conflicts, "browser_port: dropped — no browser service in the migrated stack")
		}
	}
	// The shared gateway's host port belongs to the space rendering infra —
	// a consumer space's stale slack_events_port is dead weight.
	if s.SlackEventsPort > 0 {
		if s.RendersInfra() {
			ports["slack-events"] = s.SlackEventsPort
			mapped = append(mapped, "slack_events_port -> config.ports.slack-events")
		} else {
			conflicts = append(conflicts, "slack_events_port: dropped — only the infra space publishes the shared gateway port")
		}
	}
	// channels: ingress transports carry credential refs only — the names
	// follow the broker contract the space already provisions.
	channels := map[string]any{}
	for _, ingress := range s.Ingress {
		var ch map[string]any
		switch ingress {
		case "telegram":
			ch = map[string]any{
				"bot":           map[string]any{"ref": "telegram-bot-" + s.User},
				"allowed_users": map[string]any{"ref": "telegram-allow-" + s.User},
			}
			if _, stt := svcMap["hub-stt"]; stt {
				if _, tts := svcMap["hub-tts"]; tts {
					ch["voice"] = map[string]any{"in": "transcribe", "out": "tts"}
				}
			}
		case "slack_app":
			ch = map[string]any{
				"bot":           map[string]any{"ref": "slack-app-" + s.User},
				"allowed_users": map[string]any{"ref": "slack-allow-" + s.User},
			}
		}
		if ch != nil {
			channels[ingress] = ch
		}
	}
	// mounts keep their source/mode — flattening them into the boundary
	// lists alone would lose the imperative mount contract.
	mounts := map[string]any{}
	for _, m := range s.Workspace.Mounts {
		mode := m.Mode
		if mode == "" {
			mode = "rw"
		}
		mounts[m.To] = map[string]any{"source": m.From, "mode": mode}
	}

	configDoc := map[string]any{
		"memory":  map[string]any{"backend": memoryBackend},
		"runtime": map[string]any{"timezone": s.Timezone},
	}
	if s.Context.Compact.Engine != "" {
		configDoc["runtime"].(map[string]any)["context"] = map[string]any{"compaction": s.Context.Compact.Engine}
	}
	if len(ports) > 0 {
		configDoc["ports"] = ports
	}
	if len(channels) > 0 {
		configDoc["channels"] = channels
	}
	workspaceDoc := map[string]any{"writable": toAnyStrings(writable), "read_only": toAnyStrings(readOnly), "layout": layout}
	if len(mounts) > 0 {
		workspaceDoc["mounts"] = mounts
	}

	userDocs := map[string]map[string]any{
		"stack":     {"services": svcMap},
		"config":    configDoc,
		"tools":     {"core": toolsets},
		"workspace": workspaceDoc,
		"models":    modelDocs(modelSvcs, routes, def),
	}
	mapped = append(mapped,
		"schema -> apiVersion six-passport/v2",
		"user -> stack.identity.user",
		"timezone -> config.runtime.timezone",
		"memory/honcho -> config.memory.backend",
		"tools.* -> tools.core",
		"workspace.mounts -> workspace.writable/read_only/mounts",
		"ingress -> config.channels",
		"models.yaml providers -> models.services + models.routes",
		"toolhub grants -> grants ledger",
	)
	if s.Organization != "" {
		mapped = append(mapped, "organization -> stack.identity.organization")
	}
	if s.Context.Compact.Engine != "" {
		mapped = append(mapped, "context.compact.engine -> config.runtime.context.compaction")
	}
	if s.Infra != nil {
		mapped = append(mapped, "infra -> stack.services modes and shared ports")
	}
	if s.Cliproxy != "" {
		mapped = append(mapped, "cliproxy -> stack.services instance topology")
	}
	if s.CliproxyCredentials != "" {
		mapped = append(mapped, "cliproxy_credentials -> dedicated credential plane")
	}
	if len(features) > 0 {
		conflicts = append(conflicts, fmt.Sprintf("features: %s preserved in settings (no v2 passport field)", strings.Join(features, ", ")))
	}
	// No silent drop (SPEC-0092 R8.1): every legacy key the v2 contract
	// cannot hold is reported by name so the operator decides its fate —
	// kept in settings until a passport leaf exists or retired knowingly.
	preserved := []struct {
		key string
		set bool
	}{
		{"capability_mode", s.CapabilityMode != ""},
		{"capability_profile_id", s.CapabilityProfileID != ""},
		{"capability_generation", s.CapabilityGeneration != 0},
		{"cli_registries", len(s.CLIRegistries) > 0},
		{"cli_runtime", s.CLIRuntime != ""},
		{"cli_per_principal_uid", s.CLIPerPrincipalUID},
		{"cli_toolboxes", len(s.CLIToolboxes) > 0},
		{"hosting", len(s.Hosting.Slots) > 0 || s.Hosting.Image != "" || s.Hosting.Runtime != ""},
		{"global_skills_dir", s.GlobalSkillsDir != ""},
		{"honcho_url", s.HonchoURL != ""},
		{"google_email", s.GoogleEmail != ""},
		{"gitlab_host", s.GitLabHost != ""},
		{"desktop_url", s.DesktopURL != ""},
		{"drafts_url", s.DraftsURL != ""},
		{"diagnostics", s.Diagnostics != nil},
		{"web", !reflect.DeepEqual(s.Web, stack.WebSettings{})},
		{"image_gen", s.ImageGen.Provider != "" || s.ImageGen.Model != "" || s.ImageGen.Delivery != ""},
		{"goals", s.Goals != (stack.ContextGoals{})},
		{"summary_model", s.SummaryModel != ""},
		{"judge_model", s.JudgeModel != ""},
		{"workspace.goal", s.Workspace.Goal != ""},
		{"workspace.bootstrap_revision", s.Workspace.BootstrapRevision != 0},
		{"context.compact.engines", len(s.Context.Compact.Engines) > 0},
	}
	for _, p := range preserved {
		if p.set {
			conflicts = append(conflicts, fmt.Sprintf("%s: preserved in settings (no v2 passport field)", p.key))
		}
	}
	if len(s.Hooks) > 0 {
		conflicts = append(conflicts, "hooks: preserved in settings (no passport field yet)")
	}
	// The channel vocabulary has no auth toggle leaf — an explicit
	// telegram_auth choice (either direction) cannot be expressed.
	if s.TelegramAuth != nil {
		conflicts = append(conflicts, fmt.Sprintf("telegram_auth: %v preserved in settings (no v2 passport field)", *s.TelegramAuth))
	}
	if len(s.Compression) > 0 {
		conflicts = append(conflicts, "compression: preserved in settings (no v2 passport field)")
	}

	// The generated ceiling bounds exactly what this space migrated to —
	// the platform vocabulary is the operator's own authoring later.
	ceilingTools := map[string]any{}
	for k, v := range toolsets {
		ceilingTools[k] = v
	}
	identity := map[string]any{"user": s.User}
	if s.Organization != "" {
		identity["organization"] = s.Organization
	}
	ceilingConfig := map[string]any{"memory": map[string]any{"backend": toAnyStrings([]string{"local", "honcho"})}}
	if len(ports) > 0 {
		ceilingConfig["ports"] = ports
	}
	if len(channels) > 0 {
		ceilingConfig["channels"] = channels
	}
	ceiling := passport.Layer{Name: "generated-platform-ceiling", Version: 1, Docs: map[string]map[string]any{
		"stack": {
			"identity": identity,
			"services": svcMap,
		},
		"config":    ceilingConfig,
		"tools":     {"core": ceilingTools},
		"workspace": workspaceDoc,
		"models":    {"services": modelSvcs, "routes": routes},
		"grants":    {"allow": toAnyStrings(docs.RightNames())},
	}}
	template := passport.Layer{Name: "generated-platform-template", Version: 1, Docs: map[string]map[string]any{
		"config": {"memory": map[string]any{"backend": "local"}},
	}}
	layers := []passport.Layer{ceiling, template, {}, {}, {Name: s.User, Version: 1, Docs: userDocs}}
	return layers, mapped, conflicts, nil
}

// modelDocs shapes the v2 models doc, omitting an empty default.
func modelDocs(services, routes map[string]any, def string) map[string]any {
	d := map[string]any{"services": services, "routes": routes}
	if def != "" {
		d["default"] = def
	}
	return d
}

// instanceKind resolves an emitted compose key (or a declared instance) to
// its catalog service kind — the longest hyphen-prefix that names a catalog
// infra service (cliproxy-carol -> cliproxy, hosting-alice-dev -> hosting).
// Feature entries share names with infra kinds (hosting) but carry no
// instance class — stack services resolve against the infra registry only.
func instanceKind(docs catalog.Docs, name string) (catalog.Service, string, bool) {
	for base := name; base != ""; {
		if svc, ok := docs.Services.Infra[base]; ok {
			return svc, base, true
		}
		i := strings.LastIndexByte(base, '-')
		if i < 0 {
			break
		}
		base = base[:i]
	}
	return catalog.Service{}, "", false
}

// derivedServices maps the rendered topology to stack.services instances:
// emitted keys keep their instance names, per-space modes follow who renders
// the container (a sibling renders its own relays and dedicated cliproxy;
// the infra space renders shared instances), catalog depends_on add the
// shared providers the emitted set does not already satisfy by kind, and
// the runtime stays supervised.
func derivedServices(dir string, s stack.Settings, docs catalog.Docs) (map[string]string, error) {
	modes := map[string]string{}
	servicesMap, _ := stack.Compose(s, dir, dir)["services"].(stack.M)
	for key := range servicesMap {
		name := strings.TrimSuffix(key, "-init")
		name = strings.TrimSuffix(name, "-ctl")
		if name != key {
			// -init/-ctl companions belong to their base service.
			continue
		}
		svc, kind, ok := instanceKind(docs, name)
		if !ok {
			return nil, fmt.Errorf("space-migrate: rendered service %q outside the catalog", name)
		}
		// Multi-instance cliproxy keeps its emitted name — the models
		// endpoint addresses the instance; singleton kinds collapse to the
		// bare catalog id (browser-dana -> browser, hosting-dana-dev ->
		// hosting), the compose plan re-expands it per space and env.
		instance := name
		if kind != "cliproxy" {
			instance = strings.TrimSuffix(instance, "-"+s.User+"-"+s.Environment)
			instance = strings.TrimSuffix(instance, "-"+s.User)
		}
		switch {
		case instance == "hermes-runtime":
			modes[instance] = "supervised"
		case !s.RendersInfra():
			modes[instance] = "per-space"
		case svc.Instances == "per-space" || svc.Instances == "per-space-env":
			modes[instance] = "per-space"
		default:
			modes[instance] = "shared"
		}
	}
	modes["hermes-runtime"] = "supervised"
	kindPresent := func(dep string) bool {
		for instance := range modes {
			if _, kind, ok := instanceKind(docs, instance); ok && kind == dep {
				return true
			}
		}
		return false
	}
	for added := true; added; {
		added = false
		for instance := range modes {
			svc, _, ok := instanceKind(docs, instance)
			if !ok {
				continue
			}
			for _, dep := range svc.DependsOn {
				if !kindPresent(dep) {
					modes[dep] = "shared"
					added = true
				}
			}
		}
	}
	return modes, nil
}

// derivedModels maps the merged registry to v2 services, routes and the
// default route name. cliproxy providers become cliproxy-via services
// pointing at a same-named stack instance; every other provider is an
// openai-compatible endpoint. It also returns the stack instances the
// model layer requires (a cliproxy instance per cliproxy provider).
func derivedModels(dir string, s stack.Settings, docs catalog.Docs) (stackInstances, services, routes map[string]any, def string, err error) {
	reg, err := stack.LoadModelRegistry(dir, s)
	if err != nil && errors.Is(err, stack.ErrRevisionUnloadable) {
		// Same fallback as the settings read: a stale revision must not
		// block re-migration — the legacy layered registry is the intent.
		reg, err = stack.LoadModelRegistryBase(dir, s)
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	stackInstances = map[string]any{}
	services = map[string]any{}
	routes = map[string]any{}
	seen := map[string]bool{}
	// The cliproxy stack instance is the dedicated cliproxy-<user> when the
	// space runs its own, else the shared infra alias — the models endpoint
	// must address the instance the space actually has.
	cliproxyInstance := "cliproxy"
	if s.Cliproxy == "dedicated" && !s.RendersInfra() {
		cliproxyInstance = "cliproxy-" + s.User
	}
	cliproxySpec := func() map[string]any {
		return map[string]any{
			"via":      "cliproxy",
			"endpoint": "http://" + cliproxyInstance + ":8317/v1",
			"sharing":  "personal",
			"auth": map[string]any{
				"upstream":     map[string]any{"kind": "oauth-session", "ref": cliproxyInstance},
				"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy"},
			},
		}
	}
	for name, p := range reg.Providers {
		seen[name] = true
		if name == "cliproxy" {
			services[name] = cliproxySpec()
			stackInstances[cliproxyInstance] = true
			continue
		}
		sharing := "shared"
		if len(p.BrokerGrants) > 0 {
			// personal material only exists when the broker actually grants
			// per-owner credentials — the validator refuses personal on a
			// grant-less provider.
			sharing = "personal"
		}
		spec := map[string]any{"via": "openai-compatible", "sharing": sharing}
		if p.URL != "" {
			spec["endpoint"] = p.URL
		}
		if p.CredentialEnv != "" {
			spec["auth"] = map[string]any{"upstream": map[string]any{"kind": "broker-key", "ref": p.CredentialEnv}}
		}
		services[name] = spec
	}
	// Model ids a service actually serves come off the registry ladders —
	// the passport service contract requires the cataloged list.
	served := map[string][]string{}
	addServed := func(provider, id string) {
		if id != "" && !slices.Contains(served[provider], id) {
			served[provider] = append(served[provider], id)
		}
	}
	for entryName, entry := range reg.Models {
		ladder := []any{}
		for _, hop := range entry.Ladder {
			p := hop.Provider
			if p == "cliproxy" && !seen[p] {
				seen[p] = true
				services[p] = cliproxySpec()
				stackInstances[cliproxyInstance] = true
			}
			addServed(p, hop.Model)
			for _, id := range hop.Models {
				addServed(p, id)
			}
			ladder = append(ladder, p)
		}
		routes[entryName] = map[string]any{"ladder": ladder}
	}
	for name, ids := range served {
		spec, ok := services[name].(map[string]any)
		if !ok || len(ids) == 0 {
			continue
		}
		slices.Sort(ids)
		models := make([]any, 0, len(ids))
		for _, id := range ids {
			models = append(models, map[string]any{"id": id})
		}
		spec["models"] = models
	}
	if _, ok := reg.Models["default"]; ok {
		def = "default"
	}
	return stackInstances, services, routes, def, nil
}

// grantLedger projects active toolhub grants into the capability.operation
// ledger the passport validates: a granted hub:<family> section admits every
// cataloged op of that family's capabilities. Section grants whose family the
// catalog does not know stay in the toolhub store — the ledger of record is
// never moved or rewritten — and surface as conflicts.
// selectedToolsets lists the toolset names the migrated user layer admits —
// the baseline RBAC ledger derives from their member requires.
func selectedToolsets(layers []passport.Layer) []string {
	seen := map[string]bool{}
	var out []string
	for _, area := range []string{"core", "extended"} {
		set, _ := layers[len(layers)-1].Docs["tools"][area].(map[string]any)
		for name := range set {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// grantLedger derives the RBAC grants a migrated space holds. The baseline
// is the union of member requires across every selected toolset — a space
// can always run what it was admitted to; tools.* floor is always present.
// Explicit active toolhub grants add the requires of their section's
// toolset — they can widen only inside the same vocabulary.
func grantLedger(dir string, docs catalog.Docs, layers []passport.Layer) (map[string][]string, []string, error) {
	selected := selectedToolsets(layers)
	ledger := map[string][]string{}
	addRight := func(ref string) {
		cap, op, ok := strings.Cut(ref, ".")
		if !ok {
			return
		}
		if !slices.Contains(ledger[cap], op) {
			ledger[cap] = append(ledger[cap], op)
		}
	}
	addRight("tools.list")
	addRight("tools.select")
	// Runtime floor: an admitted managed space must run its own runtime.
	// Self-inspection, self-monitoring, self-restart and reading its own
	// logs are operator guarantees every admitted space holds — without
	// them the PEP sites deny the space's own housekeeping, which is a
	// denial no operator ever granted on purpose.
	user := layers[len(layers)-1]
	svc, _ := user.Docs["stack"]["services"].(map[string]any)
	if _, managed := svc["hermes-runtime"]; managed {
		addRight("agents.inspect_self")
		addRight("agents.monitor_self")
		addRight("agents.restart_self")
		addRight("observability.read_own_logs")
	}
	// Channels are the ingress transports: an admitted channel may send,
	// and one with a voice leg may speak.
	channels, _ := user.Docs["config"]["channels"].(map[string]any)
	for _, raw := range channels {
		addRight("channels.send")
		if ch, ok := raw.(map[string]any); ok && ch["voice"] != nil {
			addRight("channels.send_voice")
		}
	}
	addToolset := func(name string) bool {
		ts, err := passport.ResolveToolset(docs, name)
		if err != nil {
			return false
		}
		for _, ops := range ts.Requires {
			for _, ref := range ops {
				addRight(ref)
			}
		}
		return true
	}
	var conflicts []string
	for _, name := range selected {
		if !addToolset(name) {
			conflicts = append(conflicts, fmt.Sprintf("admitted toolset %q has no v2 catalog entry — its member rights are not granted", name))
		}
	}
	// The legacy registry's switch policy is the models.* right: a space with
	// routes uses and lists models, and any non-"none" switch level admits
	// models.switch. Losing it would quietly demote the relay to ladders.
	// Identity comes from the compiled layers — a full stack.Read here would
	// seed runtime/toolhub/governance.json on an infra space, a write a pure
	// compile must not perform. The identity leaf is ceiling-owned.
	id, _ := layers[0].Docs["stack"]["identity"].(map[string]any)
	uname, _ := id["user"].(string)
	org, _ := id["organization"].(string)
	if reg, rErr := stack.LoadModelRegistry(dir, stack.Settings{User: uname, Organization: org}); rErr == nil {
		if len(reg.Models) > 0 {
			addRight("models.use")
			addRight("models.list")
		}
		for owner, level := range reg.Switch {
			if owner != "" && owner != uname {
				continue
			}
			if level != "none" {
				addRight("models.switch")
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "runtime", "toolhub", "governance.json")) // #nosec G304 -- operator-owned state path
	if err != nil {
		return ledger, nil, nil
	}
	var gov api.Governance
	if json.Unmarshal(b, &gov) != nil {
		return nil, nil, fmt.Errorf("space-migrate: unreadable toolhub governance store")
	}
	for _, grant := range gov.Grants {
		if grant.Status != string(api.ActiveStatus) || (!grant.ExpiresAt.IsZero() && grant.ExpiresAt.Before(time.Now())) {
			continue
		}
		if !addToolset(grant.Section) {
			conflicts = append(conflicts, fmt.Sprintf("grant %s: section %q has no v2 toolset — preserved in toolhub store", grant.GrantID, grant.Section))
		}
	}
	sort.Strings(conflicts)
	return ledger, conflicts, nil
}

// backupSources copies every editable authority file into a verified
// per-revision backup — digest recorded so a stale backup cannot masquerade
// as the rollback point. Links are refused: a backup of a symlink target is
// a backup of whatever someone pointed it at.
func backupSources(dir, revID string) ([]Object, error) {
	var out []Object
	names := []string{"settings.yaml", "agent.yaml", "workspace.yaml", "models.yaml", "SOUL.md"}
	backupDir := filepath.Join(dir, "generated", "migration-backup", revID)
	for _, name := range names {
		src := filepath.Join(dir, name)
		info, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("space-migrate: %s is not a regular file — refusing to back up a link", name)
		}
		b, err := os.ReadFile(src) // #nosec G304 -- space dir is operator scope
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		if err := os.MkdirAll(backupDir, 0700); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(filepath.Join(backupDir, name), b, 0600); err != nil {
			return nil, err
		}
		out = append(out, Object{Path: name, Files: 1, Bytes: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
	}
	manifest, _ := json.Marshal(out)
	if len(out) > 0 {
		if err := writeFileAtomic(filepath.Join(backupDir, "manifest.json"), manifest, 0600); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func toAnyStrings(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	// #nosec G703 -- every caller joins operator-owned --dir roots with
	// fixed file names; there is no untrusted path segment.
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path) // #nosec G703 -- same operator-owned join as above
}

// toolsetDisplayName renders the normative short name when it resolves to
// exactly one catalog toolset; an ambiguous short name keeps its full
// section id so the binding stays exact.
func toolsetDisplayName(docs catalog.Docs, section string) string {
	// Catalog r5 (ADR-0094 D1): native surfaces keep their namespace, while a
	// hub:<family> section is placeable under its logical name — the two can
	// share one word (terminal) without collision.
	if strings.HasPrefix(section, "native:") {
		return section
	}
	short := strings.TrimPrefix(section, "hub:")
	if short != section {
		if _, err := docs.Toolset(short); err == nil {
			return short
		}
	}
	return section
}
