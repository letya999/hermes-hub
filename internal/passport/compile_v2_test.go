package passport

import (
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
)

func v2FixtureInputs(t *testing.T) Inputs {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return Inputs{
		Space: "alice", Environment: "dev", Catalog: docs,
		Now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		PlatformCeiling: Layer{Name: "platform/ceiling", Version: 1, Docs: map[string]map[string]any{
			"stack": {
				"identity": map[string]any{"user": "alice", "organization": "acme"},
				"services": map[string]any{
					"hermes-runtime":    map[string]any{"mode": "supervised"},
					"toolhub":           map[string]any{"mode": "shared"},
					"model-relay":       map[string]any{"mode": "shared"},
					"communication-hub": map[string]any{"mode": "shared"},
					"credential-broker": map[string]any{"mode": "shared"},
					"supervisor":        map[string]any{"mode": "shared"},
					"cliproxy-google-1": map[string]any{"mode": "per-space"},
				},
			},
			"config": {
				"memory":      map[string]any{"backend": []any{"local", "vault"}},
				"supervision": map[string]any{"idle_stop": "5m", "restart": "on-failure"},
			},
			"models": {
				"services": map[string]any{
					"google-pro-1": map[string]any{"via": "cliproxy"},
				},
			},
			"workspace": {
				"writable":  []any{"/workspace", "/vault"},
				"read_only": []any{"/config", "/skills", "/shared"},
				"layout":    []any{"/workspace/work", "/workspace/inbox"},
				"mounts":    map[string]any{"/shared/acme-docs": map[string]any{"source": "org/acme/docs", "mode": "ro"}},
			},
			"tools": {
				"core":     map[string]any{"files": map[string]any{"via": "toolhub"}},
				"extended": map[string]any{"connectors": map[string]any{"via": "toolhub"}},
			},
			"grants": {"allow": []any{"workspace", "tools"}},
		}},
		PlatformTemplate: Layer{Name: "platform/personal", Version: 3, Docs: map[string]map[string]any{
			"config": {"memory": map[string]any{"backend": "local"}},
		}},
		User: Layer{Name: "alice", Version: 1, Docs: map[string]map[string]any{
			"stack": {"services": map[string]any{
				"cliproxy-google-1": map[string]any{"mode": "per-space"},
			}},
			"config": {"runtime": map[string]any{"timezone": "Europe/Moscow"},
				"ports": map[string]any{"oauth-callback": 8008}},
			"models": {
				"default": "everyday",
				"routes":  map[string]any{"everyday": map[string]any{"ladder": []any{"google-pro-1"}}},
				"services": map[string]any{
					"google-pro-1": map[string]any{
						"via":      "cliproxy",
						"endpoint": "http://cliproxy-google-1:8317/v1",
						"auth": map[string]any{
							"upstream":     map[string]any{"kind": "oauth-session", "ref": "google-one-1"},
							"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy-google-1"},
						},
						"models": []any{map[string]any{"id": "gemini-3.8-flash-high", "reasoning": "high"}},
					},
				},
			},
			"workspace": {"writable": []any{"/workspace"}},
			"tools":     {"extended": map[string]any{"connectors": map[string]any{"via": "toolhub", "tools": map[string]any{"exclude": []any{"connector_install"}}}}},
		}},
		Grants: map[string][]string{
			"workspace": {"read", "write"},
			"tools":     {"list", "use"},
		},
	}
}

func TestCompiledRevisionDecodesStrict(t *testing.T) {
	rev, err := Compile(v2FixtureInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		if _, err := Decode(name, rev.Docs[name]); err != nil {
			t.Fatalf("compiled %s does not decode as v2: %v\n%s", name, err, rev.Docs[name])
		}
	}
}

func TestConfigMemoryScheduleAndRetentionValidate(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		wantError   bool
	}{
		{"rollup", "nightly_rollup", "02:30", false},
		{"bad rollup", "nightly_rollup", "25:00", true},
		{"noncanonical rollup", "nightly_rollup", "2:30", true},
		{"retention", "leaf_retention_days", 7, false},
		{"one day", "leaf_retention_days", 1, true},
		{"fractional retention", "leaf_retention_days", 1.5, true},
		{"fold bytes", "leaf_fold_bytes", 65536, false},
		{"negative fold bytes", "leaf_fold_bytes", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := v2FixtureInputs(t)
			in.User.Docs["config"]["memory"] = map[string]any{tc.field: tc.value}
			_, err := Compile(in)
			if (err != nil) != tc.wantError {
				t.Fatalf("compile error = %v, want error %t", err, tc.wantError)
			}
		})
	}
}

func TestStackPassportNormativeShape(t *testing.T) {
	rev, err := Compile(v2FixtureInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode("stack", rev.Docs["stack"])
	if err != nil {
		t.Fatal(err)
	}
	s := doc.(*StackDoc).Stack
	if s.Identity.User != "alice" || s.Identity.Organization != "acme" {
		t.Fatalf("identity: %+v", s.Identity)
	}
	for name, want := range map[string]string{
		"hermes-runtime":    "supervised",
		"toolhub":           "shared",
		"cliproxy-google-1": "per-space",
	} {
		if got := s.Services[name].Mode; got != want {
			t.Fatalf("services.%s.mode = %q, want %q", name, got, want)
		}
	}
	if _, stray := s.Services["mode"]; stray {
		t.Fatal("legacy mode leaked into services map")
	}
}

// config.yaml is the v2 runtime contract: runtime, supervision, ports,
// channels and memory all decode into the strict view (SPEC-0092 R4.1).
func TestConfigPassportNormativeShape(t *testing.T) {
	in := v2FixtureInputs(t)
	in.PlatformCeiling.Docs["config"]["channels"] = map[string]any{
		"telegram": map[string]any{"bot": map[string]any{"ref": "x"}},
	}
	in.User.Docs["config"]["channels"] = map[string]any{
		"telegram": map[string]any{
			"bot":           map[string]any{"ref": "telegram-bot-alice"},
			"allowed_users": map[string]any{"ref": "telegram-allow-alice"},
		},
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*ConfigDoc).Config
	if c.Runtime.Timezone != "Europe/Moscow" {
		t.Fatalf("runtime.timezone lost: %+v", c.Runtime)
	}
	if c.Supervision.IdleStop != "5m" || c.Supervision.Restart != "on-failure" {
		t.Fatalf("supervision lost: %+v", c.Supervision)
	}
	if c.Ports["oauth-callback"] != 8008 {
		t.Fatalf("ports lost: %+v", c.Ports)
	}
	if c.Channels["telegram"].Bot.Ref != "telegram-bot-alice" || c.Channels["telegram"].AllowedUsers.Ref != "telegram-allow-alice" {
		t.Fatalf("channels lost refs: %+v", c.Channels)
	}
	if c.Memory.Backend != "local" {
		t.Fatalf("memory.backend lost: %+v", c.Memory)
	}
}

// connectors carries the authored raw-MCP server surface the retired
// workspace.yaml mcp: map held: the revision must keep it verbatim and
// reject malformed or reserved server definitions before publication.
func TestCompileCarriesToolsConnectors(t *testing.T) {
	in := v2FixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["extended"] = map[string]any{
		"connectors": map[string]any{"via": "toolhub"},
		"user_mcp":   map[string]any{"via": "mcp-raw"},
	}
	in.User.Docs["tools"]["connectors"] = map[string]any{
		"gitea": map[string]any{"url": "https://mcp.example.com/gitea", "timeout": 60,
			"headers": map[string]any{"X-Team": "ops"}, "tools": map[string]any{"exclude": []any{"delete_repo"}}},
		"notes": map[string]any{"command": "notes-mcp", "args": []any{"--stdio"},
			"env": map[string]any{"NOTES_DIR": "/srv"}},
	}
	in.User.Docs["tools"]["extended"] = map[string]any{
		"connectors": map[string]any{"via": "toolhub", "tools": map[string]any{"exclude": []any{"connector_install"}}},
		"user_mcp":   map[string]any{"via": "mcp-raw"},
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("tools", rev.Docs["tools"])
	if err != nil {
		t.Fatal(err)
	}
	td := raw.(*ToolsDoc)
	server, ok := td.Tools.Connectors["gitea"]
	if !ok || server.URL != "https://mcp.example.com/gitea" || server.Timeout != 60 {
		t.Fatalf("connectors lost in compile: %+v", td.Tools.Connectors)
	}
	if server.Headers["X-Team"] != "ops" || server.Tools == nil || len(server.Tools.Exclude) != 1 {
		t.Fatalf("connector fields dropped: %+v", server)
	}
	stdio := td.Tools.Connectors["notes"]
	if stdio.Command != "notes-mcp" || len(stdio.Args) != 1 || stdio.Env["NOTES_DIR"] != "/srv" {
		t.Fatalf("stdio connector lost: %+v", stdio)
	}
}

func TestCompileRejectsBadConnector(t *testing.T) {
	for _, invalid := range []any{"missing declaration", []any{"gitea"}, map[string]any{"gitea": "not a definition"}} {
		in := v2FixtureInputs(t)
		in.User.Docs["tools"]["connectors"] = invalid
		if _, err := Compile(in); err == nil {
			t.Fatal("malformed connector declaration compiled")
		}
	}
	for name, def := range map[string]map[string]any{
		"both url and command": {"url": "https://mcp.example.com/x", "command": "npx"},
		"neither":              {"timeout": 5},
		"remote with env":      {"url": "https://mcp.example.com/x", "env": map[string]any{"A": "b"}},
		"stdio with headers":   {"command": "npx", "headers": map[string]any{"A": "b"}},
		"bad url":              {"url": "not-a-url"},
		"bad auth":             {"url": "https://mcp.example.com/x", "auth": "token"},
		"bad timeout":          {"url": "https://mcp.example.com/x", "timeout": 601},
		"unknown field":        {"url": "https://mcp.example.com/x", "surprise": true},
	} {
		t.Run(name, func(t *testing.T) {
			in := v2FixtureInputs(t)
			in.User.Docs["tools"]["connectors"] = map[string]any{"gitea": def}
			if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "connector") {
				t.Fatalf("bad connector %s accepted: %v", name, err)
			}
		})
	}
}

// A connector name may never shadow a reviewed connector or a governed
// native/ToolHub toolset — the same reserved set the retired mcp: reader
// enforced.
func TestCompileRejectsReservedConnectorNames(t *testing.T) {
	for _, name := range []string{"hub", "browser", "browser_guest", "toolhub",
		"google", "telegram_user", "slack", "github", "atlassian", "desktop", "drafts",
		"terminal", "file", "web", "skills", "todo", "cronjob", "messaging", "memory",
		"session_search", "google_meet", "vision", "image_gen"} {
		t.Run(name, func(t *testing.T) {
			in := v2FixtureInputs(t)
			in.User.Docs["tools"]["connectors"] = map[string]any{
				name: map[string]any{"url": "https://mcp.example.com/x"},
			}
			if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "connector") {
				t.Fatalf("reserved connector %q accepted: %v", name, err)
			}
		})
	}
}

// A `via: off` entry is the authored self-deny: it names a connector or an
// organization catalog server the member narrows out — names unverifiable at
// compile stay admitted because a deny on nothing is a harmless no-op.
func TestCompileCarriesViaOffEntries(t *testing.T) {
	in := v2FixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["extended"] = map[string]any{
		"user_mcp":   map[string]any{"via": "mcp-raw"},
		"gitea":      map[string]any{"via": "off"},
		"clickhouse": map[string]any{"via": "off"},
	}
	in.User.Docs["tools"]["connectors"] = map[string]any{
		"gitea": map[string]any{"url": "https://mcp.example.com/gitea"},
	}
	in.User.Docs["tools"]["extended"] = map[string]any{
		"user_mcp":   map[string]any{"via": "mcp-raw"},
		"gitea":      map[string]any{"via": "off"},
		"clickhouse": map[string]any{"via": "off"},
	}
	if _, err := Compile(in); err != nil {
		t.Fatalf("via: off self-deny rejected: %v", err)
	}
	in = v2FixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["extended"] = map[string]any{
		"gitea": map[string]any{"via": "off"},
	}
	in.User.Docs["tools"]["extended"] = map[string]any{
		"gitea": map[string]any{"via": "off", "tools": map[string]any{"include": []any{"x"}}},
	}
	if _, err := Compile(in); err == nil {
		t.Fatal("via: off with selector fields accepted")
	}
}

// config.memory.provider carries the external memory-backend endpoint the
// retired settings memory_provider* fields held: adapter + absolute URL +
// env-named token, validated at compile.
func TestCompileCarriesMemoryProvider(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["config"]["memory"] = map[string]any{
		"enabled":  true,
		"provider": map[string]any{"adapter": "hindsight", "url": "http://hindsight.invalid:8888", "token_env": "HINDSIGHT_TOKEN"},
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	m := raw.(*ConfigDoc).Config.Memory
	if m.Provider == nil || m.Provider.Adapter != "hindsight" || m.Provider.URL != "http://hindsight.invalid:8888" || m.Provider.TokenEnv != "HINDSIGHT_TOKEN" {
		t.Fatalf("memory.provider lost: %+v", m.Provider)
	}
	for name, provider := range map[string]map[string]any{
		"bad adapter":   {"adapter": "other", "url": "http://x.invalid"},
		"missing url":   {"adapter": "hindsight"},
		"bad url":       {"adapter": "hindsight", "url": "not-a-url"},
		"token literal": {"adapter": "hindsight", "url": "http://x.invalid", "token_env": "lowercase-name"},
	} {
		in = v2FixtureInputs(t)
		in.User.Docs["config"]["memory"] = map[string]any{"enabled": true, "provider": provider}
		if _, err := Compile(in); err == nil {
			t.Fatalf("bad memory provider %s accepted", name)
		}
	}
}

// config.hosting carries the site-hosting contract the retired settings
// hosting: block held: unique safe slots (bounded by the exposure cap) and
// an optional digest-pinned image with an explicit sandbox runtime.
func TestCompileCarriesHosting(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["config"]["hosting"] = map[string]any{
		"slots":   []any{"site", "blog"},
		"image":   "ghcr.io/example/site@sha256:" + strings.Repeat("a", 64),
		"runtime": "runsc",
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	h := raw.(*ConfigDoc).Config.Hosting
	if h == nil || len(h.Slots) != 2 || h.Image == "" || h.Runtime != "runsc" {
		t.Fatalf("hosting lost: %+v", h)
	}
	for name, hosting := range map[string]map[string]any{
		"bad slot":       {"slots": []any{"Bad Slot"}},
		"dup slot":       {"slots": []any{"a", "a"}},
		"too many":       {"slots": []any{"a", "b", "c", "d", "e"}},
		"reserved slot":  {"slots": []any{"api"}},
		"unpinned image": {"slots": []any{"a"}, "image": "ghcr.io/example/site:latest", "runtime": "runsc"},
		"bad runtime":    {"slots": []any{"a"}, "image": "ghcr.io/example/site@sha256:" + strings.Repeat("a", 64), "runtime": "runc"},
	} {
		in = v2FixtureInputs(t)
		in.User.Docs["config"]["hosting"] = hosting
		if _, err := Compile(in); err == nil {
			t.Fatalf("bad hosting %s accepted", name)
		}
	}
}

func TestCompileCarriesImageGen(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["config"]["image_gen"] = map[string]any{"provider": "fal", "model": "chat", "delivery": "url"}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	g := raw.(*ConfigDoc).Config.ImageGen
	if g.Provider != "fal" || g.Model != "chat" || g.Delivery != "url" {
		t.Fatalf("image_gen lost: %+v", g)
	}
	for name, doc := range map[string]map[string]any{
		"bad provider": {"provider": "dalle"},
		"bad model":    {"provider": "fal", "model": "gpt-image-2"},
		"bad delivery": {"provider": "cliproxy", "delivery": "chat"},
		"not a map":    nil,
	} {
		in = v2FixtureInputs(t)
		var value any = doc
		if name == "not a map" {
			value = "fal"
		}
		in.User.Docs["config"]["image_gen"] = value
		if _, err := Compile(in); err == nil {
			t.Fatalf("bad image_gen %s accepted", name)
		}
	}
}

// The Knowledge UI loopback edge (SPEC-0085 R10.1) keeps its host port in
// config.ports.knowledge-ui — the only port whose owner is a feature
// marker (tools.knowledge_ui via native), not a declared stack service.
func TestCompileCarriesKnowledgeUIPort(t *testing.T) {
	in := v2FixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["extended"] = map[string]any{
		"memory_vault": map[string]any{"via": "native"},
		"knowledge_ui": map[string]any{"via": "native"},
	}
	in.User.Docs["config"]["ports"] = map[string]any{"knowledge-ui": 8443}
	in.User.Docs["tools"]["extended"] = map[string]any{
		"memory_vault": map[string]any{"via": "native"},
		"knowledge_ui": map[string]any{"via": "native"},
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	if raw.(*ConfigDoc).Config.Ports["knowledge-ui"] != 8443 {
		t.Fatalf("knowledge-ui port lost: %+v", raw.(*ConfigDoc).Config.Ports)
	}
	// Without the knowledge_ui marker the port names no rendered service.
	in = v2FixtureInputs(t)
	in.User.Docs["config"]["ports"] = map[string]any{"knowledge-ui": 8443}
	if _, err := Compile(in); err == nil {
		t.Fatal("knowledge-ui port accepted without knowledge_ui enabled")
	}
}

func TestCompileCarriesWorkspaceBootstrap(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["workspace"]["goal"] = "prepare-toolchain"
	in.User.Docs["workspace"]["bootstrap_revision"] = 2
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("workspace", rev.Docs["workspace"])
	if err != nil {
		t.Fatal(err)
	}
	w := raw.(*WorkspaceDoc).Workspace
	if w.Goal != "prepare-toolchain" || w.BootstrapRevision != 2 {
		t.Fatalf("workspace bootstrap lost: %+v", w)
	}
	for name, kv := range map[string]map[string]any{
		"non-string goal":   {"goal": 42},
		"non-uint revision": {"bootstrap_revision": "soon"},
		"negative revision": {"bootstrap_revision": -1},
		"fraction revision": {"bootstrap_revision": 1.5},
	} {
		in = v2FixtureInputs(t)
		for k, v := range kv {
			in.User.Docs["workspace"][k] = v
		}
		if _, err := Compile(in); err == nil {
			t.Fatalf("bad workspace bootstrap %s accepted", name)
		}
	}
}

func TestCompileRejectsM29Vocabulary(t *testing.T) {
	for _, bad := range []map[string]map[string]any{
		{"stack": {"mode": "managed"}},
		{"stack": {"features": []any{"x"}}},
		{"stack": {"environment": "dev"}},
		{"tools": {"sections": []any{"hub:files"}}},
		{"tools": {"discovery": []any{"tools_list"}}},
		{"models": {"services": []any{"cliproxy"}}},
		{"workspace": {"layout": "notalist"}},
	} {
		in := v2FixtureInputs(t)
		for doc, kv := range bad {
			for k, v := range kv {
				in.User.Docs[doc][k] = v
			}
		}
		if _, err := Compile(in); err == nil {
			t.Fatalf("legacy vocabulary accepted: %v", bad)
		}
	}
}

func TestCompileRejectsUnknownInstanceAndBadMode(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime": map[string]any{"mode": "supervised"},
		"nonsense-svc":   map[string]any{"mode": "shared"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("unknown instance: %v", err)
	}
	in = v2FixtureInputs(t)
	in.User.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime": map[string]any{"mode": "per-space"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "per-runtime") {
		t.Fatalf("bad mode accepted: %v", err)
	}
}

func TestCompileKeysetBoundOutsideCeiling(t *testing.T) {
	in := v2FixtureInputs(t)
	// Ceiling declares no "cliproxy-codex" instance; the user layer may not
	// mint it.
	in.User.Docs["stack"]["services"] = map[string]any{
		"cliproxy-codex": map[string]any{"mode": "per-space"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("service outside ceiling bound accepted: %v", err)
	}
}
