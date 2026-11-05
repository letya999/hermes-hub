package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// writeCurrentPointer stamps generated/passports/current without publishing
// the revision it names — the managed marker alone must gate the read path.
func writeCurrentPointer(t *testing.T, dir, revision string) {
	t.Helper()
	passports := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, "current"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestBrokenPublishedRevisionFailsClosed pins the authority rule: once a
// space has a current pointer, a revision that cannot fully load is a hard
// error everywhere — never a silent downgrade to the legacy files.
func TestBrokenPublishedRevisionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\nmodel: legacy-model\nmodel_url: http://cliproxy:8317/v1\noauth_port: 8000\nbrowser_port: 6080\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\ntools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeCurrentPointer(t, dir, "r-doesnotexist")
	s, err := ReadEffective(dir, "prod")
	if err == nil {
		t.Fatalf("broken published revision silently downgraded to legacy read (model=%q)", s.Model)
	}
	if !strings.Contains(err.Error(), "published revision") && !strings.Contains(err.Error(), "load effective") {
		t.Fatalf("error does not name the failed published revision: %v", err)
	}
}

// managedDocs returns the smallest complete v2 doc set a managed render can
// drive end to end: identity, the supervised runtime plus the relays it
// asserts on, a cliproxy default route, one toolset and an empty grant map.
// Tests override entries before publishing to shape their assertions.
func managedDocs(user string) map[string]string {
	infra := "false"
	if user == "infra" {
		infra = "true"
	}
	return map[string]string{
		"stack": "kind: stack\nidentity: {user: " + user + ", infra: " + infra + "}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n",
		"config":    "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n",
		"models":    "kind: models\ndefault: m\nroutes:\n  m: {ladder: [cliproxy]}\nservices:\n  cliproxy: {via: cliproxy, endpoint: http://cliproxy:8317/v1, models: [{id: m}]}\n",
		"workspace": "kind: workspace\nwritable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "kind: tools\nbinding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n",
		"grants":    "kind: grants\nallow: {}\n",
	}
}

// publishManagedDocs writes the six docs of a managed revision with their
// envelope, points current at it and refreshes the /config view the way
// Publish does — tests exercise compose against a space mid-flight between
// environments, so both env views are materialized.
func publishManagedDocs(t *testing.T, dir, user, revision string, docs map[string]string) {
	t.Helper()
	revDir := filepath.Join(dir, "generated", "passports", revision)
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	env := "apiVersion: six-passport/v2\nmetadata: {space: " + user + ", environment: dev, revision: " + revision + ", generated_at: 2026-11-02T00:00:00Z, catalogs: \"4\"}\n"
	bodies := map[string]string{}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body, ok := docs[name]
		if !ok {
			t.Fatalf("managed revision missing %s.yaml", name)
		}
		if name == "tools" && !strings.Contains(body, "binding:") {
			body += "binding: {profile_id: " + user + "-default, generation: 1}\n"
		}
		bodies[name] = body
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), []byte(env+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCurrentPointer(t, dir, revision)
	for _, envName := range []string{"dev", "prod"} {
		viewDir := filepath.Join(dir, "generated", "config-view", envName)
		if err := os.MkdirAll(viewDir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, body := range bodies {
			if err := os.WriteFile(filepath.Join(viewDir, name+".yaml"), []byte(env+body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// publishManagedRevision publishes the default managed doc set.
func publishManagedRevision(t *testing.T, dir, user, revision string) {
	t.Helper()
	publishManagedDocs(t, dir, user, revision, managedDocs(user))
}

// publishOrgRevision publishes the default managed doc set stamped with an
// organization claim — what an org member's `space apply` produces.
func publishOrgRevision(t *testing.T, dir, user, organization, revision string) {
	t.Helper()
	docs := managedDocs(user)
	docs["stack"] = strings.Replace(docs["stack"], "identity: {user: "+user, "identity: {user: "+user+", organization: "+organization, 1)
	publishManagedDocs(t, dir, user, revision, docs)
}

// publishAuthoredRevision publishes the space's own authored passports —
// the fixture equivalent of `hubctl space apply` compiling what init stamped.
func publishAuthoredRevision(t *testing.T, dir, user, revision string) {
	t.Helper()
	docs := map[string]string{}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		body, err := os.ReadFile(filepath.Join(dir, "passports", name+".yaml"))
		if err != nil {
			t.Fatalf("authored passport %s: %v", name, err)
		}
		doc := string(body)
		if !strings.Contains(doc, "kind:") {
			doc = "kind: " + name + "\n" + doc
		}
		docs[name] = doc
	}
	publishManagedDocs(t, dir, user, revision, docs)
}

// settingsToDocs renders the fields tests mutate on Settings into the six
// passport docs — the test-world successor of the retired WriteSpace. Fields
// with no passport home (arbitrary model_url, unmanaged capability state)
// simply do not appear: the six passports are the only authority now.
func settingsToDocs(s Settings) map[string]string {
	docs := managedDocs(s.User)
	var stack strings.Builder
	stack.WriteString("kind: stack\nidentity: {user: " + s.User)
	if s.Organization != "" {
		stack.WriteString(", organization: " + s.Organization)
	}
	if s.ScopeKind != "" && s.ScopeKind != "user" {
		stack.WriteString(", kind: " + s.ScopeKind)
	}
	if s.Infra != nil {
		stack.WriteString(fmt.Sprintf(", infra: %v", *s.Infra))
	}
	cliproxyMode := "shared"
	if s.Cliproxy == "dedicated" {
		cliproxyMode = "per-space"
	}
	stack.WriteString("}\nservices:\n" +
		"  hermes-runtime: {mode: supervised}\n" +
		"  model-relay: {mode: per-space}\n" +
		"  toolhub-relay: {mode: per-space}\n" +
		"  toolhub: {mode: shared}\n" +
		"  cliproxy: {mode: " + cliproxyMode + "}\n" +
		"  credential-broker: {mode: shared}\n" +
		"  supervisor: {mode: shared}\n")
	// The gateway renders only with a channel; declaring it without one
	// fails the topology check on infra spaces.
	if s.Has("telegram") || s.Has("slack_app") {
		stack.WriteString("  communication-hub: {mode: shared}\n")
	}
	// Per-space instances emit only with their feature; the plan check
	// refuses declared-but-unrendered services, so declaration follows wish.
	if s.Has("browser") || s.Has("meet") {
		stack.WriteString("  browser: {mode: per-space}\n")
	}
	if s.Has("knowledge_ui") {
		stack.WriteString("  open-knowledge: {mode: per-space}\n")
	}
	if s.Has("hosting") {
		stack.WriteString("  hosting: {mode: per-space}\n  workload-controller: {mode: shared}\n")
	}
	// hub-media is an infra-plane service: it renders only on the infra
	// space and only while declared.
	if s.Has("image_gen") {
		stack.WriteString("  hub-media: {mode: shared}\n")
	}
	docs["stack"] = stack.String()
	var config strings.Builder
	config.WriteString("kind: config\nruntime: {timezone: " + s.Timezone + "}\n")
	if len(s.Ingress) > 0 {
		config.WriteString("channels:\n")
		for _, name := range s.Ingress {
			config.WriteString("  " + name + ": {}\n")
		}
	}
	config.WriteString("memory: {enabled: " + fmt.Sprint(s.Memory))
	if s.MemoryRollupTime != "" {
		config.WriteString(", nightly_rollup: '" + s.MemoryRollupTime + "'")
	}
	if s.MemoryLeafRetentionDays > 0 {
		config.WriteString(fmt.Sprintf(", leaf_retention_days: %d", s.MemoryLeafRetentionDays))
	}
	if s.MemoryLeafFoldBytes > 0 {
		config.WriteString(fmt.Sprintf(", leaf_fold_bytes: %d", s.MemoryLeafFoldBytes))
	}
	if s.MemoryProvider != "" || s.MemoryProviderURL != "" || s.MemoryProviderTokenEnv != "" {
		config.WriteString(", provider: {adapter: " + s.MemoryProvider + `, url: "` + s.MemoryProviderURL + `"`)
		if s.MemoryProviderTokenEnv != "" {
			config.WriteString(", token_env: " + s.MemoryProviderTokenEnv)
		}
		config.WriteString("}")
	}
	config.WriteString("}\n")
	if s.ImageGen != (media.ImageGen{}) {
		fields := []string{}
		if s.ImageGen.Provider != "" {
			fields = append(fields, "provider: "+s.ImageGen.Provider)
		}
		if s.ImageGen.Model != "" {
			fields = append(fields, "model: "+s.ImageGen.Model)
		}
		if s.ImageGen.Delivery != "" {
			fields = append(fields, "delivery: "+s.ImageGen.Delivery)
		}
		config.WriteString("image_gen: {" + strings.Join(fields, ", ") + "}\n")
	}
	if len(s.Hosting.Slots) > 0 || s.Hosting.Image != "" {
		var hosting strings.Builder
		hosting.WriteString("hosting: {slots: [" + strings.Join(s.Hosting.Slots, ", ") + "]")
		if s.Hosting.Image != "" {
			hosting.WriteString(`, image: "` + s.Hosting.Image + `"`)
		}
		if s.Hosting.Runtime != "" {
			hosting.WriteString(", runtime: " + s.Hosting.Runtime)
		}
		config.WriteString(hosting.String() + "}\n")
	}
	if s.OAuthPort > 0 || s.BrowserPort > 0 {
		config.WriteString("ports: {")
		parts := []string{}
		if s.OAuthPort > 0 {
			parts = append(parts, fmt.Sprintf("oauth-callback: %d", s.OAuthPort))
		}
		if s.BrowserPort > 0 {
			parts = append(parts, fmt.Sprintf("browser-vnc: %d", s.BrowserPort))
		}
		if s.SlackEventsPort > 0 {
			parts = append(parts, fmt.Sprintf("slack-events: %d", s.SlackEventsPort))
		}
		config.WriteString(strings.Join(parts, ", ") + "}\n")
	}
	docs["config"] = config.String()
	if s.Model != "" && s.Model != "m" {
		docs["models"] = strings.Replace(docs["models"], "default: m", "default: "+s.Model, 1)
		docs["models"] = strings.Replace(docs["models"], "routes:\n  m:", "routes:\n  "+s.Model+":", 1)
		docs["models"] = strings.Replace(docs["models"], "models: [{id: m}]", "models: [{id: "+s.Model+"}]", 1)
	}
	if len(s.Tools) > 0 {
		var tools strings.Builder
		tools.WriteString("kind: tools\nbinding: {profile_id: " + s.User + "-default, generation: 1}\nextended:\n")
		names := make([]string, 0, len(s.Tools))
		for name := range s.Tools {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			entry := s.Tools[name]
			via := entry.Via
			if via == "" {
				via = ToolViaToolHub
			}
			line := "  " + name + ": {via: " + string(via)
			if via == ToolViaMCP && entry.Server != "" {
				line += ":" + entry.Server
			}
			if entry.Access == ToolAccessRO {
				line += ", access: read"
			}
			tools.WriteString(line + "}\n")
		}
		if len(s.MCP) > 0 {
			tools.WriteString("connectors:\n")
			mcpNames := make([]string, 0, len(s.MCP))
			for name := range s.MCP {
				mcpNames = append(mcpNames, name)
			}
			slices.Sort(mcpNames)
			for _, name := range mcpNames {
				server := s.MCP[name]
				tools.WriteString("  " + name + ": {")
				fields := []string{}
				if server.URL != "" {
					fields = append(fields, `url: "`+server.URL+`"`)
				}
				if server.Command != "" {
					fields = append(fields, `command: "`+server.Command+`"`)
				}
				if server.Timeout > 0 {
					fields = append(fields, fmt.Sprintf("timeout: %d", server.Timeout))
				}
				tools.WriteString(strings.Join(fields, ", ") + "}\n")
			}
		}
		docs["tools"] = tools.String()
	}
	return docs
}

// A group space carries its scope kind in stack.identity: the published
// revision — not a retired scope.yaml — tells compose and the supervisor
// that this home admits group-scoped bindings (SPEC-0091 R11.1 keeps group
// scopes usable after the legacy file is gone).
func TestManagedReadProjectsScopeKind(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("team-a")
	docs["stack"] = strings.Replace(docs["stack"], "identity: {user: team-a", "identity: {user: team-a, kind: group", 1)
	publishManagedDocs(t, dir, "team-a", "r-0123456789abcdef", docs)
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("group scope read: %v", err)
	}
	if s.ScopeKind != "group" {
		t.Fatalf("group scope kind did not project: %q", s.ScopeKind)
	}
}

// A retired scope.yaml left on disk is dead: the published identity is the
// only claim, so a contradictory stale file cannot veto the read.
func TestManagedReadIgnoresRetiredScopeFile(t *testing.T) {
	dir := t.TempDir()
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: group\nid: mallory\norganization: acme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("retired scope.yaml vetoed the published identity: %v", err)
	}
	if s.User != "alice" || s.Organization != "" || s.ScopeKind == "group" {
		t.Fatalf("retired scope.yaml leaked into identity: %+v", s.User)
	}
}

// After the cutover a published revision is the only claim: no amount of
// retired files — agent.yaml, settings.yaml, even a managed marker —
// substitutes for one (ADR-0096 D2, no legacy fallback).
func TestReadEffectiveRequiresPublishedRevision(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"settings.yaml":  "schema: 3\nuser: alice\nmodel: m\nmodel_url: http://x\ntimezone: UTC\n",
		"agent.yaml":     "schema: 3\nuser: alice\ntimezone: UTC\n",
		"workspace.yaml": "tools: {}\n",
		"scope.yaml":     "kind: user\nid: alice\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadEffective(dir, "dev"); err == nil || !strings.Contains(err.Error(), "published revision") {
		t.Fatalf("space without a published revision read cleanly: %v", err)
	}
	if err := RenderEnvironment(dir, dir, "dev"); err == nil {
		t.Fatal("render ran without a published revision")
	}
}

// An authored set without a published revision cannot start from legacy files.
func TestManagedRenderRefusesWithoutRevision(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(dir, "dev"); err == nil {
		t.Fatal("managed claim without a published revision read cleanly")
	}
	if err := RenderEnvironment(dir, dir, "dev"); err == nil {
		t.Fatal("managed claim without a published revision rendered cleanly")
	}
}

// TestManagedRenderReadsNoLegacyFile: for a managed space the retired
// authority files are dead — poisoned settings/workspace/models/agent content
// must never be opened, and the revision's values win.
func TestManagedRenderReadsNoLegacyFile(t *testing.T) {
	dir := t.TempDir()
	agent := "\x00{{{{invalid: [unclosed\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	// Poison every retired file: strict-decode garbage on disk must never be
	// consulted — a read would fail, so success proves the file stayed shut.
	for _, name := range []string{"settings.yaml", "workspace.yaml", "models.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("managed read touched a retired file or failed: %v", err)
	}
	environment, err := ReadEnvironment(dir, "dev")
	if err != nil || environment.Model != s.Model || environment.ModelURL != s.ModelURL {
		t.Fatalf("environment read diverged from the published revision: model=%q url=%q err=%v", environment.Model, environment.ModelURL, err)
	}
	if s.Infra == nil || *s.Infra {
		t.Fatalf("passport infra=false lost: %#v", s.Infra)
	}
	if s.Model != "m" || s.ModelURL != "http://model-relay:8318/v1" {
		t.Fatalf("passport model leaves not applied: model=%q url=%q", s.Model, s.ModelURL)
	}
	if !s.Has("files") {
		t.Fatalf("passport toolset surface lost: %v", s.Tools)
	}
}

func TestPublishedInfraFlagOverridesAgentYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("infra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	if SpaceRendersInfra(dir) {
		t.Fatal("agent.yaml overrode the published infra flag")
	}
}

func TestPublishedToolBindingOverridesAgentYAML(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_mode: managed\ncapability_profile_id: alice-old\ncapability_generation: 1\ninfra: false\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["tools"] = strings.Replace(docs["tools"], "alice-default, generation: 1", "alice-current, generation: 2", 1)
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.CapabilityProfileID != "alice-current" || s.CapabilityGeneration != 2 {
		t.Fatalf("agent.yaml overrode published tool binding: %q / %d", s.CapabilityProfileID, s.CapabilityGeneration)
	}
	if err := os.Remove(filepath.Join(dir, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	s, err = ReadEffectiveSpace(dir)
	if err != nil || s.CapabilityProfileID != "alice-current" || s.CapabilityGeneration != 2 {
		t.Fatalf("published binding requires agent.yaml: %q / %d, %v", s.CapabilityProfileID, s.CapabilityGeneration, err)
	}
}

// The context policy knobs the retired settings.yaml carried (compaction
// engine grants, goals, compression leaves, auxiliary models) are authored
// under config.runtime — the published revision is their only home now.
func TestManagedReadProjectsContextPolicy(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("alice")
	docs["config"] = strings.Replace(docs["config"], "runtime: {timezone: UTC}", "runtime: {timezone: UTC, context: {compaction: hub, engines: [hub, native]}, goals: {engine: native, judge: self, max_turns: 12}, compression: {threshold_tokens: 180000}, auxiliary: {summary_model: m, judge_model: m}}", 1)
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if s.Context.Compact.Engine != "hub" || len(s.Context.Compact.Engines) != 2 {
		t.Fatalf("context.compact not projected: %+v", s.Context.Compact)
	}
	if s.Goals.Engine != "native" || s.Goals.Judge != "self" || s.Goals.MaxTurns != 12 {
		t.Fatalf("goals not projected: %+v", s.Goals)
	}
	if s.Compression["threshold_tokens"] != 180000 {
		t.Fatalf("compression leaves not projected: %+v", s.Compression)
	}
	if s.SummaryModel != "m" || s.JudgeModel != "m" {
		t.Fatalf("auxiliary models not projected: %q / %q", s.SummaryModel, s.JudgeModel)
	}
	cfg := s.ManagedContextConfig()
	if cfg == nil || cfg.CompactEngine != "hub" || cfg.GoalsMaxTurns != 12 || cfg.SummaryModel != "m" {
		t.Fatalf("managed context config lost the passport policy: %+v", cfg)
	}
}

func TestManagedMemoryScheduleComesOnlyFromPassport(t *testing.T) {
	dir := t.TempDir()
	legacy := "schema: 3\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\nmemory_rollup_time: '23:00'\nmemory_leaf_retention_days: 30\nmemory_leaf_fold_bytes: 10\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("alice")
	docs["config"] += "memory: {enabled: true, nightly_rollup: '02:30', leaf_retention_days: 7, leaf_fold_bytes: 65536}\n"
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.MemoryRollupTime != "02:30" || s.MemoryLeafRetentionDays != 7 || s.MemoryLeafFoldBytes != 65536 {
		t.Fatalf("legacy agent.yaml overrode memory schedule: %q / %d / %d", s.MemoryRollupTime, s.MemoryLeafRetentionDays, s.MemoryLeafFoldBytes)
	}
}

func TestManagedReadRejectsForeignSpaceRevisionWithoutScopeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	publishManagedRevision(t, dir, "bob", "r-0123456789abcdef")
	if _, err := ReadEffectiveSpace(dir); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("foreign published identity admitted without scope.yaml: %v", err)
	}
	if SpaceRendersInfra(dir) {
		t.Fatal("foreign published identity selected the infra owner")
	}
}

// TestPointerAloneClaimsManaged: the published-revision pointer is itself
// the managed marker — an agent.yaml that never wrote capability_mode (an
// operator-edited extension file) still reads the revision only; a poisoned
// workspace.yaml on disk must never be opened.
func TestPointerAloneClaimsManaged(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("pointer-only managed space read a retired file or failed: %v", err)
	}
	if s.Model != "m" {
		t.Fatalf("passport default route lost: model=%q", s.Model)
	}
}

// TestManagedRenderIgnoresRetiredFiles: the render path itself resolves the
// managed space from its revision — a poisoned workspace.yaml left on disk
// must not fail the compose render.
func TestManagedRenderIgnoresRetiredFiles(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\ncapability_mode: managed\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("\x00{{{{invalid: [unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte("OPENAI_API_KEY=k\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "config", "SOUL.md"), []byte("# soul\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(dir, out, "dev"); err != nil {
		t.Fatalf("managed render consulted a retired file: %v", err)
	}
}

// TestManagedConnectorsProjectRawMCP: the tools passport's connectors map is
// the authored home of raw MCP servers — governed by the same user_mcp
// section the retired workspace mcp: surface answered to.
func TestManagedConnectorsProjectRawMCP(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("alice")
	docs["tools"] = "kind: tools\nbinding: {profile_id: alice-default, generation: 1}\ncore:\n  files: {via: toolhub}\nextended:\n  user_mcp: {via: mcp-raw}\nconnectors:\n  gitea: {url: \"https://mcp.example.com/gitea\", timeout: 60, tools: {exclude: [delete_repo]}}\n"
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	if _, err := ReadEffectiveSpace(dir); err == nil || !strings.Contains(err.Error(), "user_mcp") {
		t.Fatalf("declared connector admitted without a user_mcp grant: %v", err)
	}
	seedGovernance(t, dir, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", api.SectionUserMCP, time.Hour)
	})
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatalf("granted user_mcp still denied: %v", err)
	}
	server, ok := s.MCP["gitea"]
	if !ok || server.URL != "https://mcp.example.com/gitea" || server.Timeout != 60 {
		t.Fatalf("connector not projected to s.MCP: %+v", s.MCP)
	}
	if server.Tools == nil || len(server.Tools.Exclude) != 1 || server.Tools.Exclude[0] != "delete_repo" {
		t.Fatalf("connector tools filter dropped: %+v", server.Tools)
	}
	entry, ok := s.Tools["gitea"]
	if !ok || entry.Via != ToolViaMCPRaw {
		t.Fatalf("admitted user_mcp did not project a mcp-raw selection: %+v", s.Tools)
	}
	rendered := rawMCPServers(s)
	cfg, ok := rendered["gitea"]
	if !ok {
		t.Fatalf("raw MCP surface lost the admitted connector: %+v", rendered)
	}
	if m, _ := cfg.(M); m["url"] != "https://mcp.example.com/gitea" {
		t.Fatalf("rendered connector lost its url: %+v", cfg)
	}
}

// A `via: off` tools entry is the documented self-deny surface — it must
// project verbatim so org narrowing and disabledMCPNames consumers see it.
func TestManagedToolsOffProjectsDeny(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("alice")
	docs["tools"] += "extended:\n  clickhouse: {via: off}\n"
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatalf("via: off entry failed the managed read: %v", err)
	}
	if entry, ok := s.Tools["clickhouse"]; !ok || entry.Via != ToolViaOff {
		t.Fatalf("via: off not projected: %+v", s.Tools)
	}
	if !slices.Contains(s.DisabledMCPNames(), "clickhouse") {
		t.Fatalf("via: off entry missing from disabledMCPNames: %v", s.DisabledMCPNames())
	}
}

// config.memory.provider is the managed home of the retired
// memory_provider* settings fields — the published revision feeds
// validateProviderConfig and the rendered provider.json.
func TestManagedReadProjectsMemoryProvider(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("alice")
	docs["config"] = strings.Replace(docs["config"], "kind: config\n", "kind: config\nmemory: {enabled: true, provider: {adapter: hindsight, url: \"http://hindsight.invalid:8888\", token_env: HINDSIGHT_TOKEN}}\n", 1)
	docs["tools"] += "extended:\n  memory_provider: {via: native}\n"
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.MemoryProvider != "hindsight" || s.MemoryProviderURL != "http://hindsight.invalid:8888" || s.MemoryProviderTokenEnv != "HINDSIGHT_TOKEN" {
		t.Fatalf("memory provider not projected: %q %q %q", s.MemoryProvider, s.MemoryProviderURL, s.MemoryProviderTokenEnv)
	}
	if !s.Has("memory_vault") {
		t.Fatal("memory.enabled did not project the vault flag")
	}
}

// config.hosting projects the site-hosting contract into runtime Settings.
func TestManagedReadProjectsHosting(t *testing.T) {
	dir := t.TempDir()
	docs := managedDocs("alice")
	docs["config"] = strings.Replace(docs["config"], "kind: config\n",
		"kind: config\nhosting: {slots: [site, blog], image: ghcr.io/example/site@sha256:"+strings.Repeat("a", 64)+", runtime: runsc}\n", 1)
	publishManagedDocs(t, dir, "alice", "r-0123456789abcdef", docs)
	s, err := ReadEffectiveSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hosting.Slots) != 2 || s.Hosting.Slots[0] != "site" || s.Hosting.Image == "" || s.Hosting.Runtime != "runsc" {
		t.Fatalf("hosting not projected: %+v", s.Hosting)
	}
}

// TestManagedModelRegistryReadsNoLegacyLayer: under a published revision the
// relay's model registry comes from the models doc alone — a retired
// models.yaml still on disk must not contribute providers, routes or switch
// entries (SPEC-0092 R8.1).
func TestManagedModelRegistryReadsNoLegacyLayer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	agent := "schema: 3\ncapability_profile_id: alice-default\ncapability_generation: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0123456789abcdef")
	legacy := "providers:\n  ghost: {url: http://ghost.invalid/v1}\nmodels:\n  ghost-route: {ladder: [{provider: ghost}]}\n"
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadEffective(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadModelRegistry(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Providers["ghost"]; ok {
		t.Fatal("retired models.yaml leaked a provider into the managed registry")
	}
	if _, ok := reg.Models["ghost-route"]; ok {
		t.Fatal("retired models.yaml leaked a route into the managed registry")
	}
	if _, ok := reg.Providers["cliproxy"]; !ok {
		t.Fatal("passport provider lost from the managed registry")
	}
}

// TestMalformedCurrentPointerFailsClosed pins the marker rule: a present
// but non-local/traversal current id is still a published-authority claim —
// the read path refuses rather than treating the space as unmanaged.
func TestMalformedCurrentPointerFailsClosed(t *testing.T) {
	dir := t.TempDir()
	agent := "schema: 3\nuser: alice\ntimezone: UTC\nmodel: legacy-model\nmodel_url: http://cliproxy:8317/v1\noauth_port: 8000\nbrowser_port: 6080\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(agent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("schema: 3\ntools: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	passports := filepath.Join(dir, "generated", "passports")
	if err := os.MkdirAll(passports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, "current"), []byte("../escape\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEffective(dir, "prod"); err == nil {
		t.Fatal("malformed current pointer silently read as an unmanaged space")
	}
}
