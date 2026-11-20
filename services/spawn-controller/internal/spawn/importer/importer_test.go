package importer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/importer"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var tenant = store.Tenant{Org: "local", Owner: "alice"}

// fixtureSpace builds one synthetic v0 space: the real compiler emits the
// revision the way the v0 publisher wrote it, then the fixture plants the
// files the v0 writers own — never copied data, invented ids only.
type fixtureSpace struct {
	Name    string
	RevID   string
	Gen     string // v0 generation label for the runtime row, "gen-N"
	Context string
	Backups bool
	Pins    bool
}

func writeFixtureSpace(t *testing.T, spacesDir string, fs fixtureSpace, rev passport.Revision) {
	t.Helper()
	dir := filepath.Join(spacesDir, fs.Name, "generated", "passports", fs.RevID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range passport.Names {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), rev.Docs[name], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(spacesDir, fs.Name, "generated", "passports", "current"), []byte(fs.RevID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fs.Pins {
		pins := `{"envs":{"dev":{"current":{"image":"ghcr.io/acme/hermes-runtime@sha256:pin1","smoked_at":"2025-11-02T10:00:00Z"},"previous":{"image":"ghcr.io/acme/hermes-runtime@sha256:pin0","smoked_at":"2025-10-01T10:00:00Z"}}}}`
		if err := os.WriteFile(filepath.Join(spacesDir, fs.Name, "generated", "image-pins.json"), []byte(pins), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureV0 is one whole v0 tree: a control dir (supervisor.json,
// backups.json, the two journals) plus a spaces root with the emitted
// revisions.
type fixtureV0 struct {
	V0Dir     string
	SpacesDir string
}

func writeFixtureV0(t *testing.T, spaces []fixtureSpace) fixtureV0 {
	t.Helper()
	root := t.TempDir()
	v0 := fixtureV0{V0Dir: filepath.Join(root, "control"), SpacesDir: filepath.Join(root, "spaces")}
	if err := os.MkdirAll(v0.V0Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{}
	for _, fs := range spaces {
		items = append(items, map[string]any{
			"runtime": map[string]any{
				"principal_id": fs.Name, "context_id": fs.Context,
				"runtime_mode": "agent", "generation": fs.Gen,
				"runtime_id": "rt-" + fs.Name, "desired": true,
				"state": "ready", "container": "hermes-" + fs.Name,
				"last_used": "2025-11-03T10:00:00Z",
			},
		})
		writeFixtureSpace(t, v0.SpacesDir, fs, compileFixture(t, fs.Name))
	}
	manager := map[string]any{
		"execution_backend": "process", "generation": 41, "lease_seq": 9,
		"items": items,
		"jobs":  map[string]any{"job-1": map[string]any{"status": "done"}},
		"holds": map[string]any{"alice:main": map[string]any{"active": true}},
		"pins":  map[string]any{"alice:main:agent": map[string]any{"text": "x"}},
	}
	writeJSONFile(t, filepath.Join(v0.V0Dir, "supervisor.json"), manager)
	var backups []map[string]any
	for _, fs := range spaces {
		if !fs.Backups {
			continue
		}
		backups = append(backups, map[string]any{
			"user": fs.Name, "last_success_at": "2025-11-04T03:00:00Z",
			"last_archive": "backup-20251104T030000Z.zip", "retained": 3,
			"last_drill_at": "2025-11-04T04:00:00Z", "drill_result": "ok", "drill_checked": 12,
		})
	}
	writeJSONFile(t, filepath.Join(v0.V0Dir, "backups.json"), backups)
	for file, lines := range map[string][]string{
		"signals.jsonl": {
			`{"at":"2025-11-04T01:00:00Z","class":"crash-loop","principal_id":"alice","context_id":"main","runtime_id":"rt-a","generation":"gen-7"}`,
			`{"at":"2025-11-04T02:00:00Z","class":"metrics-breach","principal_id":"alice","context_id":"main","runtime_id":"rt-a","generation":"gen-7"}`,
		},
		"transitions.jsonl": {
			`{"at":"2025-11-04T00:00:00Z","kind":"runtime","id":"alice:main:agent","principal_id":"alice","context_id":"main","generation":"gen-7","phase":"starting"}`,
			`{"at":"2025-11-04T00:10:00Z","kind":"runtime","id":"alice:main:agent","principal_id":"alice","context_id":"main","generation":"gen-7","phase":"ready"}`,
			`{"at":"2025-11-04T03:00:00Z","kind":"job","id":"job-1","principal_id":"alice","context_id":"main","phase":"done"}`,
		},
	} {
		if err := os.WriteFile(filepath.Join(v0.V0Dir, file), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return v0
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// compileFixture emits the space's current revision through the real
// compiler — the bytes a v0 publisher wrote, not a hand-invented shape.
// The six-layer input mirrors the revision service's normative fixture.
func compileFixture(t *testing.T, user string) passport.Revision {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	layers := fixtureLayers(user)
	folded, ledger, err := passport.LayeredGrants(layers)
	if err != nil {
		t.Fatalf("fixture grants: %v", err)
	}
	rev, err := passport.Compile(passport.Inputs{
		Space:            user,
		Environment:      "dev",
		PlatformCeiling:  folded[0],
		PlatformTemplate: folded[1],
		OrgCeiling:       folded[2],
		OrgTemplate:      folded[3],
		User:             folded[4],
		Agent:            folded[5],
		Grants:           ledger,
		Catalog:          docs,
		Now:              time.Unix(1_800_000_000, 0),
	})
	if err != nil {
		t.Fatalf("fixture compile: %v", err)
	}
	return rev
}

func fixtureLayers(user string) []passport.Layer {
	return []passport.Layer{
		{Name: "platform-ceiling", Version: 1, Docs: map[string]map[string]any{
			"stack": {
				"identity": map[string]any{"user": user, "organization": "local"},
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
			"config": {"memory": map[string]any{"backend": []any{"local", "honcho"}}},
			"tools": {"core": map[string]any{
				"native:file": map[string]any{"via": "native"},
				"files":       map[string]any{"via": "toolhub"},
				"terminal":    map[string]any{"via": "toolhub"},
			}},
			"workspace": {
				"writable":  []any{"/workspace", "/vault"},
				"read_only": []any{"/config", "/skills", "/shared"},
			},
			"models": {
				"services": map[string]any{
					"cliproxy":   map[string]any{"via": "cliproxy"},
					"direct-key": map[string]any{"via": "openai-compatible"},
				},
				"routes": map[string]any{"everyday": map[string]any{"ladder": []any{"cliproxy"}}},
			},
			"grants": {"allow": []any{"workspace", "tools", "config"}},
		}},
		{Name: "platform-template", Version: 1, Docs: map[string]map[string]any{
			"config": {
				"memory":  map[string]any{"backend": "local"},
				"runtime": map[string]any{"timezone": "UTC"},
				"ports":   map[string]any{"browser-vnc": 6080},
				"supervision": map[string]any{
					"watchers": map[string]any{"nightly-rollup": map[string]any{"protected": true}},
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
		}},
		{Name: "org-ceiling", Version: 1, Docs: map[string]map[string]any{
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": []any{"workspace", "tools"}},
		}},
		{Name: "org-template", Version: 1, Docs: map[string]map[string]any{
			"config":    {"memory": map[string]any{"backend": "honcho"}},
			"workspace": {"writable": []any{"/workspace"}},
		}},
		{Name: "user", Version: 1, Docs: map[string]map[string]any{
			"config": {"memory": map[string]any{"backend": "local"}, "runtime": map[string]any{"timezone": "Europe/Berlin"}},
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": map[string]any{
				"workspace": []any{"read", "write"},
				"tools":     []any{"list", "use"},
			}},
		}},
		{Name: "agent", Version: 1, Docs: map[string]map[string]any{}},
	}
}

func testImporter(t *testing.T, url string) (*importer.Importer, *spawn.Revisions) {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	st := storetest.Open(t, url, migrations.FS)
	revs := &spawn.Revisions{Store: st, LoadCatalog: func() (catalog.Docs, error) { return docs, nil }}
	return &importer.Importer{Revisions: revs, Store: st, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}, revs
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", strings.TrimPrefix(path, root), info.Mode())
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func spacesFixture(t *testing.T) fixtureV0 {
	return writeFixtureV0(t, []fixtureSpace{
		{Name: "alice", RevID: "r-fixturealice01", Gen: "gen-7", Context: "main", Backups: true, Pins: true},
		{Name: "bob", RevID: "r-fixturebob0002", Gen: "gen-12", Context: "main", Backups: true, Pins: true},
	})
}

func TestImporterDryRunWritesNothing(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, revs := testImporter(t, url)
		fix := spacesFixture(t)
		rep, err := imp.Run(context.Background(), importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir, DryRun: true})
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		if !rep.Ran || !rep.DryRun || len(rep.Spaces) != 2 || len(rep.Failed) != 0 {
			t.Fatalf("dry report %+v", rep)
		}
		if rep.SignalsDropped != 2 || rep.TransitionsDropped != 3 || rep.RuntimesDropped != 2 {
			t.Fatalf("dropped counts %+v", rep)
		}
		for _, space := range []string{"alice", "bob"} {
			if _, err := revs.Effective(context.Background(), store.Tenant{Org: "local", Owner: space}, space); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("dry run wrote space %s: %v", space, err)
			}
		}
	})
}

func TestImporterIdempotent(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, revs := testImporter(t, url)
		fix := spacesFixture(t)
		ctx := context.Background()
		rep, err := imp.Run(ctx, importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir})
		if err != nil || len(rep.Spaces) != 2 {
			t.Fatalf("first run: %v %+v", err, rep)
		}
		eff, err := revs.Effective(ctx, tenant, "alice")
		if err != nil {
			t.Fatalf("effective: %v", err)
		}
		second, err := imp.Run(ctx, importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir})
		if err != nil {
			t.Fatalf("second run: %v", err)
		}
		if second.Ran || second.Reason != "already imported" || len(second.Spaces) != 0 {
			t.Fatalf("second run not a no-op: %+v", second)
		}
		again, err := revs.Effective(ctx, tenant, "alice")
		if err != nil || again.Digest != eff.Digest {
			t.Fatalf("digest drifted: %v %s vs %s", err, again.Digest, eff.Digest)
		}
	})
}

func TestImporterMapsCurrentRevisionToActiveUserLayer(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, revs := testImporter(t, url)
		fix := spacesFixture(t)
		ctx := context.Background()
		rep, err := imp.Run(ctx, importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(rep.Spaces) != 2 || rep.Revisions != 2 || rep.Backups != 2 || rep.ImagePins == 0 {
			t.Fatalf("report %+v", rep)
		}
		rows, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil || len(rows) != 1 {
			t.Fatalf("user revisions: %v %d", err, len(rows))
		}
		if rows[0].State != "active" || rows[0].Layer != "user" {
			t.Fatalf("revision %+v", rows[0])
		}
		eff, err := revs.Effective(ctx, tenant, "alice")
		if err != nil || eff.Digest == "" || len(eff.Documents) != len(passport.Names) {
			t.Fatalf("effective %+v: %v", eff, err)
		}
		var stack map[string]any
		if err := json.Unmarshal(eff.Documents["stack"], &stack); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(stack["kind"]) != "stack" {
			t.Fatalf("emitted stack doc missing envelope: %v", stack)
		}
		// The imported spec feeds explain: every leaf the import wrote is
		// attributed to the user layer or the compiler's derived surface.
		ex, err := revs.Explain(ctx, tenant, "alice")
		if err != nil || len(ex.Fields) == 0 {
			t.Fatalf("explain: %v %+v", err, ex)
		}
		for _, f := range ex.Fields {
			if f.Layer != "user" && f.Layer != passport.DerivedLayer {
				t.Fatalf("unexpected layer %q on %s.%s", f.Layer, f.Doc, f.Path)
			}
		}
	})
}

func TestImporterKeepsGenerationsMonotonic(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, _ := testImporter(t, url)
		fix := spacesFixture(t)
		ctx := context.Background()
		rep, err := imp.Run(ctx, importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if rep.Generations != 2 {
			t.Fatalf("baseline rows %d", rep.Generations)
		}
		var gen int64
		err = imp.Store.ReadTx(ctx, func(tx *store.Tx) error {
			return tx.Scoped(tenant).QueryRow("SELECT COALESCE(MAX(generation),0) FROM runtimes WHERE {tenant} AND space_id = ? AND agent = ?", "alice", "main").Scan(&gen)
		})
		if err != nil {
			t.Fatal(err)
		}
		if gen != 7 {
			t.Fatalf("baseline generation %d, want 7", gen)
		}
	})
}

func TestImporterNeverTouchesV0Files(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, _ := testImporter(t, url)
		fix := spacesFixture(t)
		before := hashTree(t, fix.V0Dir) + hashTree(t, fix.SpacesDir)
		if _, err := imp.Run(context.Background(), importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if after := hashTree(t, fix.V0Dir) + hashTree(t, fix.SpacesDir); after != before {
			t.Fatal("v0 fixture tree changed")
		}
	})
}

func TestImporterReportHasNoContents(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		imp, _ := testImporter(t, url)
		fix := spacesFixture(t)
		rep, err := imp.Run(context.Background(), importer.Config{V0Dir: fix.V0Dir, SpacesDir: fix.SpacesDir})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		body, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		// The report must never carry document values from the fixtures —
		// not a field value, not a doc body, not env contents.
		for _, needle := range []string{
			"Europe/Berlin", "browser-vnc", "cliproxy-main", "everyday",
			"nightly-rollup", "oauth-session", "backup-20251104T030000Z.zip",
			"crash-loop", "hermes-alice",
		} {
			if strings.Contains(string(body), needle) {
				t.Fatalf("report leaks %q: %s", needle, body)
			}
		}
	})
}

func TestExplainAttributesLayer(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		_, revs := testImporter(t, url)
		ctx := context.Background()
		if _, err := revs.CreateSpace(ctx, tenant, "alice", "dev"); err != nil {
			t.Fatal(err)
		}
		// All six layers submit through the service the way the plan's
		// fixture does — submissions all land as drafts first, then each
		// validates against the pending docs of the layers not yet live.
		var ids []string
		for _, layer := range fixtureLayers("alice") {
			if len(layer.Docs) == 0 {
				continue
			}
			rev, err := revs.Submit(ctx, tenant, "alice", layer.Name, layer.Docs)
			if err != nil {
				t.Fatalf("submit %s: %v", layer.Name, err)
			}
			ids = append(ids, rev.ID)
		}
		var all []string
		for _, id := range ids {
			if _, err := revs.Validate(ctx, tenant, "alice", id); err != nil {
				t.Fatalf("validate %s: %v", id, err)
			}
			all = append(all, id)
		}
		if _, err := revs.Activate(ctx, tenant, "alice", all); err != nil {
			t.Fatalf("activate: %v", err)
		}
		rep, err := revs.Explain(ctx, tenant, "alice")
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if rep.Digest == "" || rep.Space != "alice" {
			t.Fatalf("report %+v", rep)
		}
		got := map[string]string{}
		for _, f := range rep.Fields {
			got[f.Doc+"."+f.Path] = f.Layer
		}
		if got["config.runtime.timezone"] != "user" {
			t.Fatalf("timezone attributed to %q, want user", got["config.runtime.timezone"])
		}
		if got["stack.identity.user"] != "platform-ceiling" {
			t.Fatalf("identity attributed to %q, want platform-ceiling", got["stack.identity.user"])
		}
		if got["workspace.tree"] != passport.DerivedLayer {
			t.Fatalf("tree attributed to %q, want derived", got["workspace.tree"])
		}
	})
}
