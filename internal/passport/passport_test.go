package passport_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

func fixtureInputs(t *testing.T) passport.Inputs {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatalf("catalog generate: %v", err)
	}
	return passport.Inputs{
		Space:       "alice",
		Environment: "dev",
		Now:         time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		Catalog:     docs,
		PlatformCeiling: passport.Layer{
			Name: "platform-ceiling", Version: 1,
			Docs: map[string]map[string]any{
				"stack": {
					"identity": map[string]any{"user": "alice", "organization": "acme"},
					"services": map[string]any{
						"hermes-runtime":    map[string]any{"mode": "supervised"},
						"browser":           map[string]any{"mode": "per-space"},
						"toolhub":           map[string]any{"mode": "shared"},
						"model-relay":       map[string]any{"mode": "shared"},
						"communication-hub": map[string]any{"mode": "shared"},
						"credential-broker": map[string]any{"mode": "shared"},
						"supervisor":        map[string]any{"mode": "shared"},
						"cliproxy":          map[string]any{"mode": "per-space"},
						"hub-stt":           map[string]any{"mode": "shared"},
						"hub-tts":           map[string]any{"mode": "shared"},
					},
				},
				"config": {
					"memory": map[string]any{"backend": []any{"local", "honcho"}},
				},
				"tools": {"core": map[string]any{
					"native:file": map[string]any{"via": "native"},
					"files":       map[string]any{"via": "toolhub"},
					"terminal":    map[string]any{"via": "toolhub"},
				}},
				"workspace": {
					"writable":  []any{"/workspace", "/vault"},
					"read_only": []any{"/config", "/skills", "/shared"},
					"layout": []any{
						"/workspace/inbox/people", "/workspace/inbox/services",
						"/workspace/work", "/workspace/projects",
						"/workspace/artifacts/documents", "/workspace/artifacts/images",
						"/workspace/artifacts/videos", "/workspace/artifacts/archives",
						"/workspace/artifacts/services", "/workspace/outbox",
					},
				},
				"models": {
					"services": map[string]any{
						"cliproxy":   map[string]any{"via": "cliproxy"},
						"direct-key": map[string]any{"via": "openai-compatible"},
					},
					"routes": map[string]any{
						"everyday": map[string]any{"ladder": []any{"cliproxy"}},
					},
				},
				"grants": {"allow": []any{"workspace", "tools", "config"}},
			},
		},
		PlatformTemplate: passport.Layer{
			Name: "base", Version: 1,
			Docs: map[string]map[string]any{
				"config": {
					"memory":  map[string]any{"backend": "local"},
					"runtime": map[string]any{"timezone": "UTC"},
					"ports":   map[string]any{"browser-vnc": 6080},
					"supervision": map[string]any{
						"watchers": map[string]any{
							"nightly-rollup": map[string]any{"protected": true},
						},
					},
				},
				"tools":     {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
				"workspace": {"writable": []any{"/workspace"}, "read_only": []any{"/config", "/skills"}},
				"models": {
					"default": "everyday",
					"services": map[string]any{
						"cliproxy": map[string]any{
							"via":      "cliproxy",
							"endpoint": "http://cliproxy:8317/v1",
							"auth": map[string]any{
								"upstream":     map[string]any{"kind": "oauth-session", "ref": "cliproxy-main"},
								"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy"},
							},
						},
					},
				},
			},
		},
		OrgCeiling: passport.Layer{
			Name: "acme-ceiling", Version: 3,
			Docs: map[string]map[string]any{
				"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
				"grants": {"allow": []any{"workspace", "tools"}},
			},
		},
		OrgTemplate: passport.Layer{
			Name: "acme-base", Version: 2,
			Docs: map[string]map[string]any{
				"config":    {"memory": map[string]any{"backend": "honcho"}},
				"workspace": {"writable": []any{"/workspace"}},
			},
		},
		User: passport.Layer{
			Name: "alice", Version: 1,
			Docs: map[string]map[string]any{
				"config": {"memory": map[string]any{"backend": "local"}, "runtime": map[string]any{"timezone": "Europe/Berlin"}},
				"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}}},
			},
		},
		Grants: map[string][]string{
			"workspace": {"read", "write"},
			"tools":     {"list", "use"},
		},
	}

}

func compileFixture(t *testing.T) passport.Revision {
	t.Helper()
	rev, err := passport.Compile(fixtureInputs(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rev
}

// The agent layer merges below the user layer and may only narrow it:
// every leaf it writes must already exist in the merged result — a list
// may shrink to a subset, a scalar must repeat the value. Anything else is
// a widening and fails the compile (ADR-0106).
func TestAgentLayerOnlyNarrows(t *testing.T) {
	agent := func(docs map[string]map[string]any) passport.Layer {
		return passport.Layer{Name: "agent:alice:nova", Version: 1, Docs: docs}
	}
	cases := []struct {
		name string
		docs map[string]map[string]any
		err  string
	}{
		{"narrow list subset", map[string]map[string]any{
			"workspace": {"writable": []any{"/workspace"}},
		}, ""},
		{"widen narrow list", map[string]map[string]any{
			"workspace": {"writable": []any{"/workspace", "/etc"}},
		}, "beyond the user layer"},
		{"narrowed-away ceiling path", map[string]map[string]any{
			"workspace": {"writable": []any{"/vault"}},
		}, "beyond the user layer"},
		{"repeat scalar", map[string]map[string]any{
			"config": {"runtime": map[string]any{"timezone": "Europe/Berlin"}},
		}, ""},
		{"change scalar", map[string]map[string]any{
			"config": {"runtime": map[string]any{"timezone": "UTC"}},
		}, "beyond the user layer"},
		{"add absent leaf", map[string]map[string]any{
			"config": {"runtime": map[string]any{"goals": map[string]any{"max_turns": 3}}},
		}, "beyond the user layer"},
		{"keyset repeat", map[string]map[string]any{
			"tools": {"core": map[string]any{"native:file": map[string]any{"via": "native"}}},
		}, ""},
		{"keyset change", map[string]map[string]any{
			"tools": {"core": map[string]any{"native:file": map[string]any{"via": "toolhub"}}},
		}, "beyond the user layer"},
		{"keyset new entry", map[string]map[string]any{
			"tools": {"core": map[string]any{"evil": map[string]any{"via": "toolhub"}}},
		}, "beyond the user layer"},
		{"stack new service", map[string]map[string]any{
			"stack": {"services": map[string]any{"evil": map[string]any{"mode": "per-space"}}},
		}, "beyond the user layer"},
		{"grants extra cap", map[string]map[string]any{
			"grants": {"allow": []any{"workspace", "secrets"}},
		}, "beyond the user layer"},
		{"layout narrows ro", map[string]map[string]any{
			"workspace": {"layout": []any{map[string]any{"path": "/workspace/work", "mode": "ro"}}},
		}, ""},
		{"layout new path", map[string]map[string]any{
			"workspace": {"layout": []any{"/workspace/secret"}},
		}, "outside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := fixtureInputs(t)
			in.Agent = agent(tc.docs)
			if tc.name == "grants extra cap" || tc.name == "narrow list subset" || tc.name == "widen narrow list" || tc.name == "narrowed-away ceiling path" || tc.name == "layout narrows ro" || tc.name == "layout new path" {
				in.Grants = map[string][]string{"workspace": {"read", "write"}, "tools": {"list", "use"}}
			}
			_, err := passport.Compile(in)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("agent narrowing must compile: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("agent widening must fail with %q, got %v", tc.err, err)
			}
		})
	}
	// A narrowing subset still narrows grants below the merged ceiling:
	// the merged cap list is the org ceiling's {workspace, tools}; the
	// agent picks a subset and the trimmed ledger stays inside it.
	in := fixtureInputs(t)
	in.Grants = map[string][]string{"workspace": {"read", "write"}}
	in.Agent = agent(map[string]map[string]any{
		"grants": {"allow": []any{"workspace"}},
	})
	if _, err := passport.Compile(in); err != nil {
		t.Fatalf("agent grants subset must compile: %v", err)
	}
}

func TestCompiledPolicyCarriesOneSpaceAndRevision(t *testing.T) {
	rev := compileFixture(t)
	policy, err := rev.PolicyBundle()
	if err != nil || policy.Space != "alice" || policy.Environment != "dev" || policy.PassportRevision != rev.ID || len(policy.Passports) != 6 {
		t.Fatalf("compiled policy lost its scope or revision: %v", err)
	}
	rev.Docs["grants"] = bytes.Replace(rev.Docs["grants"], []byte(rev.ID), []byte("other-revision"), 1)
	if _, err := rev.PolicyBundle(); err == nil {
		t.Fatal("a mixed revision was eligible for policy signing")
	}
}

func TestPassportCompilerAtomicSixFileRevision(t *testing.T) {
	rev := compileFixture(t)

	// One revision, six passports, common envelope.
	for _, name := range passport.Names {
		if len(rev.Docs[name]) == 0 {
			t.Fatalf("revision %s missing passport %s", rev.ID, name)
		}
	}
	if rev.ID == "" {
		t.Fatal("empty revision id")
	}
	if rev.Provenance.Space != "alice" || rev.Provenance.Environment != "dev" || rev.Provenance.Revision != rev.ID {
		t.Fatalf("metadata mismatch: %+v", rev.Provenance)
	}
	if rev.Provenance.Template["platform"] != "base@1" || rev.Provenance.Template["organization"] != "acme-base@2" {
		t.Fatalf("template provenance missing: %+v", rev.Provenance.Template)
	}
	if rev.Provenance.Sources["platform-ceiling"] != 1 || rev.Provenance.Sources["acme-ceiling"] != 3 {
		t.Fatalf("source provenance missing: %+v", rev.Provenance.Sources)
	}
	if rev.Provenance.Catalogs["capabilities"] != catalog.Release || rev.Provenance.Catalogs["toolsets"] != catalog.Release || rev.Provenance.Catalogs["services"] != catalog.Release {
		t.Fatalf("catalog pins missing: %+v", rev.Provenance.Catalogs)
	}
	if rev.Provenance.UpdatedAt.IsZero() || rev.Provenance.GeneratedAt.IsZero() {
		t.Fatal("revision timestamps missing")
	}

	// Field classes resolve: invariant holds, org narrowed the ceiling,
	// the user narrowed further, choice stayed inside the offered set.
	got := func(doc, path string) any {
		t.Helper()
		v, err := rev.Value(doc, path)
		if err != nil {
			t.Fatalf("%s.%s: %v", doc, path, err)
		}
		return v
	}
	if got("stack", "identity.user") != "alice" || got("stack", "identity.organization") != "acme" {
		t.Fatalf("invariant identity overwritten: %v", got("stack", "identity"))
	}
	if got("stack", "services.hermes-runtime.mode") != "supervised" {
		t.Fatalf("runtime mode wrong: %v", got("stack", "services"))
	}
	if got("tools", "core.native:file.via") != "native" {
		t.Fatalf("user toolset selection lost: %v", got("tools", "core"))
	}
	if got("config", "memory.backend") != "local" {
		t.Fatalf("choice config.memory.backend wrong: %v", got("config", "memory.backend"))
	}
	if got("config", "runtime.timezone") != "Europe/Berlin" {
		t.Fatalf("user_data timezone wrong: %v", got("config", "runtime"))
	}
	allow, err := rev.GrantAllow()
	if err != nil {
		t.Fatalf("grants allow: %v", err)
	}
	if len(allow) != 2 || len(allow["workspace"]) != 2 {
		t.Fatalf("ledger snapshot not carried into grants: %+v", allow)
	}

	// Deterministic revision id for identical inputs.
	again, err := passport.Compile(fixtureInputs(t))
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if again.ID != rev.ID {
		t.Fatalf("revision id not deterministic: %s vs %s", again.ID, rev.ID)
	}

	// The six-file publish pipeline is gone (S7d): activation is the
	// service-side effect; generated trees no longer exist.
}

// Templates never carry grant authority (ADR-0092 D3): a grants doc in a
// template layer fails the whole compile.
func TestPassportTemplateCarriesNoGrants(t *testing.T) {
	in := fixtureInputs(t)
	in.OrgTemplate.Docs["grants"] = map[string]any{"allow": []any{"workspace"}}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("template layer carrying grants compiled")
	}
	in = fixtureInputs(t)
	in.PlatformTemplate.Docs["grants"] = map[string]any{"allow": []any{"workspace"}}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("platform template carrying grants compiled")
	}
}

// Fail-closed grant edges (SPEC-0091 R5.1/R2.2): a ledger entry outside the
// ceiling, an operation the catalog capability does not declare, a model
// default outside the declared services, or a field the shared schema does
// not classify all reject the revision before publish.
func TestPassportCompileRejectsInvalidEdges(t *testing.T) {
	in := fixtureInputs(t)
	in.Grants["workspace"] = []string{"read", "delete"}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("undeclared operation compiled into grants")
	}
	// A catalog-valid grant still cannot widen past the merged ceiling:
	// allow compiles only from trusted ceilings ∩ active ledger (R5.2).
	in = fixtureInputs(t)
	in.Grants["files.search"] = []string{"search"}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("ledger grant outside the ceiling compiled")
	}
	in = fixtureInputs(t)
	in.PlatformTemplate.Docs["models"]["default"] = "direct-key"
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("models.default outside services compiled")
	}
	in = fixtureInputs(t)
	in.OrgTemplate.Docs["stack"] = map[string]any{"audit": "off"}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("unclassified field compiled")
	}
}

// Defense in depth on the catalog boundary: when a ceiling names an
// inadmissible universe (a cataloged but denied-by-construction toolset, or
// a name the catalog does not know) the compile rejects the revision even
// though the selection fits the declared bound.
func TestPassportCatalogBoundaryRejected(t *testing.T) {
	in := fixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["core"].(map[string]any)["native:bot_room"] = map[string]any{"via": "native"}
	in.OrgCeiling.Docs["tools"]["core"].(map[string]any)["native:bot_room"] = map[string]any{"via": "native"}
	in.User.Docs["tools"]["core"] = map[string]any{"native:bot_room": map[string]any{"via": "native"}}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("denied-by-construction toolset admitted into tools.core")
	}
	in = fixtureInputs(t)
	in.PlatformCeiling.Docs["tools"]["core"].(map[string]any)["native:ghost"] = map[string]any{"via": "native"}
	in.OrgCeiling.Docs["tools"]["core"].(map[string]any)["native:ghost"] = map[string]any{"via": "native"}
	in.User.Docs["tools"]["core"] = map[string]any{"native:ghost": map[string]any{"via": "native"}}
	if _, err := passport.Compile(in); err == nil {
		t.Fatal("unknown toolset admitted into tools.core")
	}
}

// The stack/config split (R3): stack.yaml names the service instances and
// config.yaml assigns their host ports. A port for a service that is not
// running, or two services sharing one host port, fails at compile time —
// before any compose change (A3).
func TestStackConfigRejectsMissingServiceAndPortCollision(t *testing.T) {
	rev := compileFixture(t)

	// The user can inspect which services run and how they are configured.
	services, err := rev.Value("stack", "services")
	if err != nil {
		t.Fatalf("stack.services: %v", err)
	}
	running := map[string]bool{}
	for s := range services.(map[string]any) {
		running[s] = true
	}
	if !running["hermes-runtime"] || !running["browser"] {
		t.Fatalf("stack.services = %v, want hermes-runtime+browser", services)
	}
	if port, err := rev.Value("config", "ports.browser-vnc"); err != nil || fmt.Sprint(port) != "6080" {
		t.Fatalf("config.ports.browser-vnc = %v %v, want 6080", port, err)
	}

	// Port-owner derivation (catalog port table) is S4.1's; the port map
	// itself already enforces host-port uniqueness.

	// Two services claiming one host port fail before compose.
	collide := fixtureInputs(t)
	collide.User.Docs["config"] = map[string]any{
		"ports": map[string]any{"browser-vnc": 6080, "oauth-callback": 6080},
	}
	if _, err := passport.Compile(collide); err == nil {
		t.Fatal("host port collision compiled")
	}

	// A service the platform catalog does not know fails the plan.
	ghost := fixtureInputs(t)
	ghost.User.Docs["stack"] = map[string]any{
		"services": map[string]any{"ghost-svc": map[string]any{"mode": "per-space"}},
	}
	if _, err := passport.Compile(ghost); err == nil {
		t.Fatal("uncataloged service admitted into stack.services")
	}

	// Dependencies are derived from the catalog — a running service
	// without its catalog-declared dependencies fails the plan:
	// hermes-runtime needs toolhub, model-relay and communication-hub,
	// none of which run here.
	thin := fixtureInputs(t)
	thin.PlatformCeiling.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime": map[string]any{"mode": "supervised"}, "browser": map[string]any{"mode": "per-space"},
	}
	if _, err := passport.Compile(thin); err == nil {
		t.Fatal("service without its catalog dependencies compiled")
	}

	// A space without hermes-runtime is simply unmanaged — valid v2, the
	// runtime's presence is what makes a space managed.
	noRuntime := fixtureInputs(t)
	noRuntime.PlatformCeiling.Docs["stack"]["services"] = map[string]any{
		"browser":  map[string]any{"mode": "per-space"},
		"cliproxy": map[string]any{"mode": "per-space"},
	}
	if _, err := passport.Compile(noRuntime); err != nil {
		t.Fatalf("unmanaged space without hermes-runtime: %v", err)
	}

	// A voice channel compiles while the speech services run.
	voice := fixtureInputs(t)
	voice.User.Docs["config"] = map[string]any{
		"channels": map[string]any{"telegram": map[string]any{"voice": map[string]any{"in": "transcribe", "out": "tts"}}},
	}
	if _, err := passport.Compile(voice); err != nil {
		t.Fatalf("voice channel with speech services: %v", err)
	}

	// A voice channel without the speech services fails the plan: hub-stt
	// and hub-tts must run before any compose promises voice.
	noVoice := fixtureInputs(t)
	noVoice.PlatformCeiling.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime":    map[string]any{"mode": "supervised"},
		"toolhub":           map[string]any{"mode": "shared"},
		"model-relay":       map[string]any{"mode": "shared"},
		"communication-hub": map[string]any{"mode": "shared"},
		"credential-broker": map[string]any{"mode": "shared"},
		"supervisor":        map[string]any{"mode": "shared"},
	}
	noVoice.User.Docs["config"] = map[string]any{
		"channels": map[string]any{"telegram": map[string]any{"voice": map[string]any{"in": "transcribe", "out": "tts"}}},
	}
	if _, err := passport.Compile(noVoice); err == nil {
		t.Fatal("voice channel without speech services compiled")
	}

	// Dropping only the speech synthesis side still fails the plan.
	noTTS := fixtureInputs(t)
	svcs := map[string]any{}
	for name, spec := range noTTS.PlatformCeiling.Docs["stack"]["services"].(map[string]any) {
		if name != "hub-tts" {
			svcs[name] = spec
		}
	}
	noTTS.PlatformCeiling.Docs["stack"]["services"] = svcs
	noTTS.User.Docs["config"] = map[string]any{
		"channels": map[string]any{"telegram": map[string]any{"voice": map[string]any{"in": "transcribe", "out": "tts"}}},
	}
	if _, err := passport.Compile(noTTS); err == nil {
		t.Fatal("voice channel without hub-tts compiled")
	}

	// Ordinary user watchers coexist with the protected stack watchers the
	// platform declared; the protected entries stay in the effective view.
	withOwn := fixtureInputs(t)
	withOwn.User.Docs["config"] = map[string]any{
		"supervision": map[string]any{
			"watchers": map[string]any{"my-job": map[string]any{"schedule": "0 9 * * *"}},
		},
	}
	revOwn, err := passport.Compile(withOwn)
	if err != nil {
		t.Fatalf("user watcher alongside protected ones: %v", err)
	}
	if p, err := revOwn.Value("config", "supervision.watchers.nightly-rollup.protected"); err != nil || p != true {
		t.Fatalf("protected watcher lost: %v %v", p, err)
	}

	// Flipping the protected flag off is a user override of service
	// authority and fails the compile.
	unprotect := fixtureInputs(t)
	unprotect.User.Docs["config"] = map[string]any{
		"supervision": map[string]any{
			"watchers": map[string]any{"nightly-rollup": map[string]any{"protected": false}},
		},
	}
	if _, err := passport.Compile(unprotect); err == nil {
		t.Fatal("user dropped a protected watcher")
	}
}

// The workspace passport (R4): writable and read_only are disjoint
// canonical mount boundaries; the ASCII tree is derived, never written by
// a layer.
func TestWorkspacePassportBoundaries(t *testing.T) {
	// A path cannot be both writable and read_only in one revision.
	conflict := fixtureInputs(t)
	conflict.User.Docs["workspace"] = map[string]any{
		"writable":  []any{"/workspace/inbox"},
		"read_only": []any{"/workspace/inbox"},
	}
	if _, err := passport.Compile(conflict); err == nil {
		t.Fatal("path both writable and read_only compiled")
	}

	// A non-canonical boundary path escapes nothing: traversal rejects.
	trav := fixtureInputs(t)
	trav.User.Docs["workspace"] = map[string]any{
		"writable": []any{"/workspace/../config"},
	}
	if _, err := passport.Compile(trav); err == nil {
		t.Fatal("traversal path admitted into workspace.writable")
	}
}

// The ASCII tree is derived from the approved layout (ADR-0092 D5):
// every declared dir renders, and no layer may write the field itself.
func TestWorkspaceTreeDerivedFromLayout(t *testing.T) {
	rev := compileFixture(t)
	tree, err := rev.Value("workspace", "tree")
	if err != nil {
		t.Fatalf("workspace.tree: %v", err)
	}
	text, _ := tree.(string)
	for _, want := range []string{"inbox", "artifacts", "outbox"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace.tree missing %q:\n%s", want, text)
		}
	}
}

// R6.1/A6: tools.yaml projects whole toolsets — core never exceeds twenty
// selected tools and never splits a family, overflow lands in extended in
// deterministic catalog order, the platform layer stays pinned in core, and
// the catalog pin makes a release bump a visible diff rather than a silent
// growth.
func TestToolsetCoreLimitAndExtendedInvokeRevoke(t *testing.T) {
	rev := compileFixture(t)
	core, err := rev.Value("tools", "core")
	if err != nil {
		t.Fatalf("tools.core: %v", err)
	}
	coreMap, _ := core.(map[string]any)
	if len(coreMap) == 0 {
		t.Fatal("core selection empty")
	}
	if _, ok := coreMap["native:file"]; !ok {
		t.Fatalf("user toolset selection lost: %v", coreMap)
	}
	platform, _ := coreMap["platform"].(map[string]any)
	if fmt.Sprint(platform["via"]) != "toolhub" {
		t.Fatalf("platform layer not pinned in core: %v", coreMap)
	}
	if _, leaked := coreMap["discovery"]; leaked {
		t.Fatalf("legacy discovery pseudo-toolset leaked into core: %v", coreMap)
	}

	// A toolset whose members would overflow the core budget moves to
	// extended wholesale — no family is ever split.
	in := fixtureInputs(t)
	for _, fat := range []string{"assignments", "goals", "sessions", "watches"} {
		in.PlatformCeiling.Docs["tools"]["core"].(map[string]any)[fat] = map[string]any{"via": "toolhub"}
		in.OrgCeiling.Docs["tools"]["core"].(map[string]any)[fat] = map[string]any{"via": "toolhub"}
	}
	in.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"assignments": map[string]any{"via": "toolhub"},
		"goals":       map[string]any{"via": "toolhub"},
		"sessions":    map[string]any{"via": "toolhub"},
		"watches":     map[string]any{"via": "toolhub"},
	}
	fat, err := passport.Compile(in)
	if err != nil {
		t.Fatalf("compile with oversized toolset: %v", err)
	}
	extended, err := fat.Value("tools", "extended")
	if err != nil {
		t.Fatalf("tools.extended: %v", err)
	}
	extMap, _ := extended.(map[string]any)
	if _, ok := extMap["watches"]; !ok {
		t.Fatalf("overflowing toolset not moved to extended: %v", extMap)
	}
	core2, _ := fat.Value("tools", "core")
	if _, inCore := core2.(map[string]any)["watches"]; inCore {
		t.Fatal("overflow toolset stayed in core — family split")
	}

	// R6.2: a restriction is admitted only where the routed executor
	// provably enforces it — include and exclude are exclusive, members
	// must be real, and nothing a native/raw-MCP surface cannot honour
	// may be claimed.
	restricted := fixtureInputs(t)
	restricted.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"files": map[string]any{
			"via":    "toolhub",
			"access": "read",
			"tools":  map[string]any{"exclude": []any{"file_write"}},
		},
	}
	okRev, err := passport.Compile(restricted)
	if err != nil {
		t.Fatalf("enforceable toolset restriction rejected: %v", err)
	}
	hf, _ := okRev.Value("tools", "core.files")
	if fmt.Sprint(hf.(map[string]any)["access"]) != "read" {
		t.Fatalf("access restriction lost: %v", hf)
	}

	for name, spec := range map[string]map[string]any{
		"both include and exclude": {
			"via": "toolhub", "tools": map[string]any{"include": []any{"file_read"}, "exclude": []any{"file_write"}},
		},
		"unknown member": {
			"via": "toolhub", "tools": map[string]any{"exclude": []any{"file_teleport"}},
		},
		"unsupported scope": {
			"via": "toolhub", "scope": map[string]any{"galactic": []any{"x"}},
		},
	} {
		bad := fixtureInputs(t)
		bad.User.Docs["tools"]["core"] = map[string]any{
			"native:file": map[string]any{"via": "native"},
			"files":       spec,
		}
		if _, err := passport.Compile(bad); err == nil {
			t.Fatalf("%s compiled", name)
		}
	}

	// Native carveouts cannot enforce a tool-level restriction — claiming
	// one fails closed rather than silently admitting it.
	native := fixtureInputs(t)
	native.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native", "access": "read"},
	}
	if _, err := passport.Compile(native); err == nil {
		t.Fatal("unenforceable native restriction compiled")
	}
}

// R10: skill and connector surfaces project through the cataloged toolsets —
// one source of truth — while raw MCP stays default-denied by construction:
// only the reviewed user_mcp/org_mcp sections are placeable, an ad-hoc
// mcp-raw:<server> name can never compile, and skill/connector mutations
// need their cataloged operations in the ledger.
func TestSkillConnectorPassportProjectionAndRawMCPDenial(t *testing.T) {
	in := fixtureInputs(t)
	offer := map[string]any{
		"native:file": map[string]any{"via": "native"},
		"files":       map[string]any{"via": "toolhub"},
		"skills":      map[string]any{"via": "toolhub"},
		"connectors":  map[string]any{"via": "toolhub"},
		"user_mcp":    map[string]any{"via": "mcp-raw"},
		"org_mcp":     map[string]any{"via": "mcp-raw"},
	}
	in.PlatformCeiling.Docs["tools"]["core"] = offer
	in.OrgCeiling.Docs["tools"] = map[string]any{"core": offer}
	in.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"skills":      map[string]any{"via": "toolhub"},
		"connectors":  map[string]any{"via": "toolhub"},
		"org_mcp":     map[string]any{"via": "mcp-raw"},
	}
	in.PlatformCeiling.Docs["grants"]["allow"] = []any{"workspace", "skills"}
	in.OrgCeiling.Docs["grants"]["allow"] = []any{"workspace", "skills"}
	in.Grants = map[string][]string{"workspace": {"read"}, "skills": {"draft"}}

	rev, err := passport.Compile(in)
	if err != nil {
		t.Fatalf("cataloged skill/connector/mcp-raw toolsets must compile: %v", err)
	}
	core, err := rev.Value("tools", "core")
	if err != nil {
		t.Fatal(err)
	}
	coreMap, _ := core.(map[string]any)
	extended, err := rev.Value("tools", "extended")
	if err != nil {
		t.Fatal(err)
	}
	extMap, _ := extended.(map[string]any)
	// The pinned platform family occupies six of the twenty-member core
	// budget, so an admitted toolset may land in extended — the projection
	// must carry it either way.
	for _, want := range []string{"skills", "connectors", "org_mcp"} {
		if _, ok := coreMap[want]; !ok {
			if _, ok := extMap[want]; !ok {
				t.Fatalf("compiled projection lost admitted toolset %s: core=%v extended=%v", want, coreMap, extMap)
			}
		}
	}

	// Denied by construction: a raw server name can never mint its own
	// toolset — only the reviewed user_mcp/org_mcp names resolve.
	bad := fixtureInputs(t)
	bad.PlatformCeiling.Docs["tools"]["core"] = offer
	bad.User.Docs["tools"]["core"] = map[string]any{"mcp-raw:gitea": map[string]any{"via": "mcp-raw"}}
	if _, err := passport.Compile(bad); err == nil {
		t.Fatal("an ad-hoc mcp-raw:<server> toolset must fail compilation")
	}
	// And a cataloged-but-not-offered toolset cannot be self-admitted:
	// org_mcp is outside the ceiling key set.
	wide := fixtureInputs(t)
	wide.User.Docs["tools"]["core"] = map[string]any{
		"native:file": map[string]any{"via": "native"},
		"org_mcp":     map[string]any{"via": "mcp-raw"},
	}
	if _, err := passport.Compile(wide); err == nil {
		t.Fatal("a user layer writing a toolset outside the ceiling must fail")
	}

	// Draft/publish needs the cataloged operation: a ledger inventing an
	// undocumented skill_manage operation fails closed.
	draft := in
	draft.Grants = map[string][]string{"skills_manage.manage": {"publish_anything"}}
	if _, err := passport.Compile(draft); err == nil {
		t.Fatal("an uncataloged skills_manage operation must fail compilation")
	}
}

// The v2 envelope (SPEC-0092): kind carries the passport name, contract keys
// are top-level — no body wrapper — and provenance fields (updated_at,
// sources) live only in the revision's provenance.yaml.
func TestPublishedFileHasNoBodyWrapper(t *testing.T) {
	rev := compileFixture(t)
	eff, err := rev.Effective()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := spawnv1.EncodeDocuments(eff)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range passport.Names {
		b := docs[name]
		var head struct {
			APIVersion string         `yaml:"apiVersion"`
			Kind       string         `yaml:"kind"`
			Metadata   map[string]any `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(b, &head); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if head.APIVersion != "six-passport/v2" || head.Kind != name {
			t.Fatalf("%s envelope: %q %q", name, head.APIVersion, head.Kind)
		}
		if _, ok := head.Metadata["sources"]; ok {
			t.Fatalf("%s leaks sources provenance into the passport", name)
		}
		if _, ok := head.Metadata["updated_at"]; ok {
			t.Fatalf("%s leaks updated_at into the passport", name)
		}
		var whole map[string]any
		if err := yaml.Unmarshal(b, &whole); err != nil {
			t.Fatal(err)
		}
		if _, ok := whole["body"]; ok {
			t.Fatalf("%s still wraps contract keys in body:", name)
		}
	}
	// Provenance rides the revision record, not a published side file (S7d):
	// the source-layer tally lives on Revision.Provenance.
	if len(rev.Provenance.Sources) == 0 {
		t.Fatal("revision lost the source-layer record")
	}
}
