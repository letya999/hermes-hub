package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

// TestM30MigrationEmitsFullContract is the anti-degradation golden: a
// migrated space publishes all six passports in the normative v2 form —
// per-service stack modes, full config, real routes, a workspace tree,
// toolset vocabulary and a non-empty RBAC ledger — and nothing the source
// carried is silently dropped.
func TestM30MigrationEmitsFullContract(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "spaces", "carol")
	if err := os.MkdirAll(filepath.Join(dir, "runtime", "toolhub"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := "schema: 3\nuser: carol\ntimezone: Europe/Moscow\nmodel: default\nmodel_url: http://model-relay:8318/v1\noauth_port: 8101\nbrowser_port: 8102\ngitlab_host: gitlab.example.test\ntools:\n  files:\n    via: toolhub\n  terminal:\n    via: toolhub\n  memory:\n    via: native\ningress:\n  - telegram\nworkspace:\n  mounts:\n    - from: shared\n      to: /shared/data\n      mode: ro\n"
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	models := "switch:\n    carol: catalog\nmodels:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: gemini-3-flash\n    alt:\n        ladder:\n            - provider: cliproxy\n              model: gemini-3-flash\n"
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(models), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev", Apply: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	doc := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rep.Revision, name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := yaml.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		if d["apiVersion"] != "six-passport/v2" {
			t.Fatalf("%s: wrong apiVersion %v", name, d["apiVersion"])
		}
		if d["kind"] != name {
			t.Fatalf("%s: wrong kind %v", name, d["kind"])
		}
		if _, has := d["body"]; has {
			t.Fatalf("%s: forbidden body: wrapper", name)
		}
		if _, has := d["metadata"]; !has {
			t.Fatalf("%s: missing metadata", name)
		}
		return d
	}

	st := doc("stack")
	if _, bad := st["features"]; bad {
		t.Fatalf("stack still carries legacy features: %v", st["features"])
	}
	id, _ := st["identity"].(map[string]any)
	if org, has := id["organization"]; has && org == "" {
		t.Fatal("stack.identity carries an empty organization key")
	}
	services, _ := st["services"].(map[string]any)
	if len(services) == 0 {
		t.Fatal("stack.services empty")
	}
	for name, raw := range services {
		spec, _ := raw.(map[string]any)
		if spec["mode"] == nil || spec["mode"] == "" {
			t.Fatalf("stack.services.%s has no per-service mode: %v", name, spec)
		}
	}
	cfg := doc("config")
	rt, _ := cfg["runtime"].(map[string]any)
	if rt["timezone"] != "Europe/Moscow" {
		t.Fatalf("config lost timezone: %v", rt)
	}
	ports, _ := cfg["ports"].(map[string]any)
	if ports["oauth-callback"] != 8101 || ports["browser-vnc"] != 8102 {
		t.Fatalf("config lost ports: %v", ports)
	}
	if _, has := cfg["channels"].(map[string]any)["telegram"]; !has {
		t.Fatal("config lost the telegram channel")
	}
	md := doc("models")
	routes, _ := md["routes"].(map[string]any)
	if len(routes) != 2 || routes["default"] == nil || routes["alt"] == nil {
		t.Fatalf("models lost routes: %v", routes)
	}
	svcs, _ := md["services"].(map[string]any)
	cx, _ := svcs["cliproxy"].(map[string]any)
	if cx["via"] != "cliproxy" || cx["sharing"] != "personal" || cx["endpoint"] == "" {
		t.Fatalf("models lost cliproxy service detail: %v", cx)
	}
	if ml, _ := cx["models"].([]any); len(ml) != 1 {
		t.Fatalf("models lost the served model list: %v", cx["models"])
	}
	ws := doc("workspace")
	if _, bad := ws["layout"]; bad {
		t.Fatal("workspace still carries the removed layout key")
	}
	if !strings.Contains(ws["tree"].(string), "/workspace") {
		t.Fatalf("workspace lost the materialized tree: %v", ws["tree"])
	}
	mounts, _ := ws["mounts"].(map[string]any)
	m, _ := mounts["/shared/data"].(map[string]any)
	if m["source"] != "shared" || m["mode"] != "ro" {
		t.Fatalf("workspace lost the external mount: %v", mounts)
	}
	tools := doc("tools")
	core, _ := tools["core"].(map[string]any)
	files, _ := core["files"].(map[string]any)
	if files["via"] != "toolhub" {
		t.Fatalf("files is not a catalog toolset entry: %v", files)
	}
	if _, has := files["scope"]; !has {
		t.Fatal("files lost its read scope")
	}
	rules, _ := files["rules"].(map[string]any)
	fw, _ := rules["file_write"].(map[string]any)
	if _, has := fw["scope"]; !has {
		t.Fatalf("files lost its write boundary rules: %v", rules)
	}
	gr := doc("grants")
	allow, _ := gr["allow"].(map[string]any)
	if len(allow) == 0 {
		t.Fatal("grants allow is empty")
	}
	ops, _ := allow["workspace"].([]any)
	var flat []string
	for _, op := range ops {
		flat = append(flat, op.(string))
	}
	if !contains(flat, "read") || !contains(flat, "write") {
		t.Fatalf("grants lost member-level requires (want workspace.read+write): %v", allow)
	}
	if _, ok := allow["models"]; !ok {
		t.Fatalf("grants lost the model routes rights: %v", allow)
	}
	// Runtime floor: the space may inspect, monitor and restart itself and
	// send on its admitted ingress channel.
	agentOps, _ := allow["agents"].([]any)
	flat = flat[:0]
	for _, op := range agentOps {
		flat = append(flat, op.(string))
	}
	for _, want := range []string{"inspect_self", "monitor_self", "restart_self"} {
		if !contains(flat, want) {
			t.Fatalf("grants lost the runtime floor %q: %v", want, allow)
		}
	}
	if _, ok := allow["channels"]; !ok {
		t.Fatalf("grants lost the admitted channel right: %v", allow)
	}

	// The published revision decodes strictly and drives the runtime view.
	eff, err := passport.LoadEffective(dir)
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if eff.Header.Metadata.Revision != rep.Revision {
		t.Fatalf("effective revision %q != published %q", eff.Header.Metadata.Revision, rep.Revision)
	}
	// Conflicts must surface what the contract cannot hold — never silent.
	if len(rep.Conflicts) == 0 {
		t.Fatal("migration reported no conflicts for settings it cannot represent")
	}
	// Retired sources: after deleting them the passport alone answers.
	for _, f := range []string{"settings.yaml", "models.yaml"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := stack.Read(dir)
	if err != nil {
		t.Fatalf("passport-only Read: %v", err)
	}
	if s.User != "carol" || s.Timezone != "Europe/Moscow" || s.Model != "default" || s.OAuthPort != 8101 {
		t.Fatalf("Read dropped passport values: %+v", s)
	}
	if len(s.Tools) == 0 || s.Tools["files"].Via != stack.ToolViaToolHub {
		t.Fatalf("Read dropped the toolset surface: %v", s.Tools)
	}
	reg, err := stack.LoadModelRegistry(dir, s)
	if err != nil {
		t.Fatalf("LoadModelRegistry: %v", err)
	}
	if len(reg.Models) != 2 || reg.Switch["carol"] != "catalog" {
		t.Fatalf("registry dropped the passport routes/switch: %+v", reg)
	}
}

// A dedicated cliproxy topology must migrate honestly: the space's own
// cliproxy container is a per-space instance named after the emitted
// service, the models endpoint addresses that instance — not the shared
// infra alias it never had.
func TestM30DedicatedCliproxyMigratesPerSpaceInstance(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "spaces", "carol")
	if err := os.MkdirAll(filepath.Join(dir, "runtime", "toolhub"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := "schema: 3\nuser: carol\ntimezone: UTC\nmodel: default\nmodel_url: http://model-relay:8318/v1\noauth_port: 8101\nbrowser_port: 8102\ninfra: false\ncliproxy: dedicated\ntools:\n  files:\n    via: toolhub\n"
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	models := "models:\n    default:\n        ladder:\n            - provider: cliproxy\n              model: gemini-3-flash\n"
	if err := os.WriteFile(filepath.Join(dir, "models.yaml"), []byte(models), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev", Apply: true})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	readDoc := func(name string) map[string]any {
		b, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rep.Revision, name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := yaml.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	services, _ := readDoc("stack")["services"].(map[string]any)
	inst, ok := services["cliproxy-carol"].(map[string]any)
	if !ok || inst["mode"] != "per-space" {
		t.Fatalf("dedicated cliproxy must migrate as per-space instance cliproxy-carol: %v", services)
	}
	if _, ok := services["cliproxy"]; ok {
		t.Fatalf("shared cliproxy must not appear beside the dedicated instance: %v", services)
	}
	modelSvcs, _ := readDoc("models")["services"].(map[string]any)
	cx, _ := modelSvcs["cliproxy"].(map[string]any)
	if cx["endpoint"] != "http://cliproxy-carol:8317/v1" {
		t.Fatalf("models endpoint must address the dedicated instance, got %v", cx["endpoint"])
	}
}

// TestM30CheckCompleteRefusesSkeleton proves a managed revision cannot be
// published as a shell: no toolsets beyond discovery, no writable paths or
// empty config all fail the compile before staging.
func TestM30CheckCompleteRefusesSkeleton(t *testing.T) {
	docs, err := catalog.LoadPinned()
	if err != nil {
		t.Skipf("pinned catalogs unavailable: %v", err)
	}
	managed := func() passport.Inputs {
		return passport.Inputs{
			Space:       "carol",
			Environment: "dev",
			Catalog:     docs,
			Grants:      map[string][]string{"tools": {"list", "select"}},
			PlatformCeiling: passport.Layer{Name: "ceiling", Version: 1, Docs: map[string]map[string]any{
				"stack": {
					"identity": map[string]any{"user": "carol"},
					"services": map[string]any{
						"hermes-runtime":    map[string]any{"mode": "supervised"},
						"cliproxy":          map[string]any{"mode": "shared"},
						"communication-hub": map[string]any{"mode": "shared"},
						"credential-broker": map[string]any{"mode": "shared"},
						"egress-relay":      map[string]any{"mode": "shared"},
						"model-relay":       map[string]any{"mode": "shared"},
						"supervisor":        map[string]any{"mode": "shared"},
						"toolhub":           map[string]any{"mode": "shared"},
						"toolhub-relay":     map[string]any{"mode": "shared"},
					},
				},
				"config": {"runtime": map[string]any{"timezone": "UTC"}},
				"models": {
					"default": "default",
					"routes":  map[string]any{"default": map[string]any{"ladder": []any{"cliproxy"}}},
					"services": map[string]any{"cliproxy": map[string]any{
						"via":      "cliproxy",
						"endpoint": "http://cliproxy:8317/v1",
						"auth":     map[string]any{"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy"}},
						"models":   []any{map[string]any{"id": "m1"}},
						"sharing":  "personal",
					}},
				},
				"workspace": {"writable": []any{"/workspace"}, "read_only": []any{"/config"}, "layout": []any{"/workspace"}},
				"tools":     {"core": map[string]any{"files": map[string]any{"via": "toolhub"}}},
				"grants":    {"allow": []any{"tools", "agents", "channels", "observability"}},
			}},
		}
	}
	if _, err := passport.Compile(managed()); err != nil {
		t.Fatalf("full managed layer failed compile: %v", err)
	}
	skeleton := managed()
	skeleton.PlatformCeiling.Docs["tools"] = map[string]any{"core": map[string]any{}}
	if _, err := passport.Compile(skeleton); err == nil || !strings.Contains(err.Error(), "admits no toolsets") {
		t.Fatalf("skeleton tools compiled: %v", err)
	}
	skeleton = managed()
	skeleton.PlatformCeiling.Docs["workspace"]["writable"] = []any{}
	if _, err := passport.Compile(skeleton); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("empty writable compiled: %v", err)
	}
	skeleton = managed()
	skeleton.PlatformCeiling.Docs["config"] = map[string]any{}
	if _, err := passport.Compile(skeleton); err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("empty config compiled: %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// DeriveSpaceRights on a secondary space must land in the shared control
// plane — the infra sibling's store + governance ledger — never in a
// space-local store the shared ToolHub never reads (SPEC-0092 R2.1/2.2).
func TestDeriveSpaceRightsSharedInfraStore(t *testing.T) {
	spaces := filepath.Join(t.TempDir(), "spaces")
	infra := filepath.Join(spaces, "infra-a")
	sibling := filepath.Join(spaces, "sib-one")
	for _, dir := range []string{infra, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(infra, "agent.yaml"), []byte("user: infra-a\ninfra: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "agent.yaml"), []byte("user: sib-one\ninfra: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(infra, "runtime", "toolhub")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := toolhub.NewStore().Save(filepath.Join(root, "store.json")); err != nil {
		t.Fatal(err)
	}
	docs := catalog.Docs{Rights: api.RightsDoc{Capabilities: map[string]api.RightCapability{
		"workspace": {Operations: map[string]api.RightOperation{
			"read": {Subject: "self", Issuers: []string{"platform"}},
		}},
	}}}
	rev := passport.Revision{Docs: map[string][]byte{"grants": []byte("allow:\n  workspace:\n    - read\n")}}
	if err := DeriveSpaceRights(sibling, "sib-one", rev, docs, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sibling, "runtime", "toolhub")); !os.IsNotExist(err) {
		t.Fatalf("secondary space grew a space-local store: %v", err)
	}
	gov, err := api.LoadGovernance(filepath.Join(root, "governance.json"))
	if err != nil {
		t.Fatal(err)
	}
	grants := 0
	for _, g := range gov.Rights {
		if g.PrincipalID == "sib-one" && g.Capability == "workspace" && g.Operation == "read" && g.Status == api.GrantActive {
			grants++
		}
	}
	if grants != 1 {
		t.Fatalf("shared ledger must hold one active derived grant: %d %+v", grants, gov.Rights)
	}
	// A space with no control plane anywhere still skips cleanly.
	lonely := filepath.Join(t.TempDir(), "lonely")
	if err := os.MkdirAll(lonely, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lonely, "agent.yaml"), []byte("user: lonely\ninfra: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DeriveSpaceRights(lonely, "lonely", rev, docs, time.Now().UTC()); err != nil {
		t.Fatalf("unmanaged sibling must skip derivation, got: %v", err)
	}
}

// Re-migration must not trust a stale published revision for topology:
// when the operator profile still claims a dedicated cliproxy, the
// derived services map emits the per-space instance even though the
// previous passport already says shared.
func TestM30RemigrateHonorsProfileDedicatedCliproxy(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "spaces", "dana")
	revDir := filepath.Join(dir, "generated", "passports", "r-0123456789abcdef")
	if err := os.MkdirAll(revDir, 0700); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef}\n"
	docs := map[string]string{
		"stack.yaml":     head + "kind: stack\nidentity: {user: dana}\nservices:\n    cliproxy: {mode: shared}\n    hermes-runtime: {mode: supervised}\n    model-relay: {mode: shared}\n",
		"config.yaml":    head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8002, browser-vnc: 6081}\n",
		"models.yaml":    head + "kind: models\ndefault: default\nroutes: {default: {ladder: [cliproxy]}}\nservices:\n    cliproxy:\n        via: cliproxy\n        endpoint: http://cliproxy:8317/v1\n        sharing: personal\n        models: [{id: gemini-3-flash}]\n",
		"workspace.yaml": head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n",
		"tools.yaml":     head + "kind: tools\ncore:\n    files: {via: toolhub}\n",
		"grants.yaml":    head + "kind: grants\nallow:\n    workspace: [read]\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(revDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "passports", "current"), []byte("r-0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\ncapability_mode: managed\ncapability_profile_id: dana-default\ncapability_generation: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "runtime", "toolhub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "spaces", "profiles.yaml"), []byte("profiles:\n    owners:\n        owners: [dana]\n        cliproxy: dedicated\n        cliproxy_credentials: private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := MigratePassport(PassportOptions{Dir: dir, Environment: "dev", Apply: true})
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rep.Revision, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := yaml.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	services, _ := st["services"].(map[string]any)
	inst, ok := services["cliproxy-dana"].(map[string]any)
	if !ok || inst["mode"] != "per-space" {
		t.Fatalf("profile intent must migrate as per-space cliproxy-dana: %v", services)
	}
	if _, ok := services["cliproxy"]; ok {
		t.Fatalf("stale shared cliproxy survived re-migration: %v", services)
	}
}
