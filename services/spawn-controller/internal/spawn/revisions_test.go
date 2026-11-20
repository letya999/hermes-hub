package spawn_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

func testRevisions(t *testing.T, url string) *spawn.Revisions {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatalf("catalog generate: %v", err)
	}
	// Distinct increasing timestamps keep created_at ordering deterministic
	// — the retention test asserts the oldest rows are the pruned tail.
	clock := time.Unix(1_800_000_000, 0)
	return &spawn.Revisions{
		Store:   storetest.Open(t, url, migrations.FS),
		Catalog: docs,
		Now:     func() time.Time { clock = clock.Add(time.Second); return clock },
	}
}

// layerDocs mirrors the normative passport fixture's layer documents: a
// managed hermes-runtime space needs the platform ceilings and templates,
// the org layers and the user layer for the compile to complete.
func layerDocs() map[string]map[string]map[string]any {
	return map[string]map[string]map[string]any{
		"platform-ceiling": {
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
		"platform-template": {
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
		"org-ceiling": {
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": []any{"workspace", "tools"}},
		},
		"org-template": {
			"config":    {"memory": map[string]any{"backend": "honcho"}},
			"workspace": {"writable": []any{"/workspace"}},
		},
	}
}

// userDocs is the user layer; timezone varies between revisions so the
// content digest differs while staying inside every ceiling.
func userDocs(timezone string) map[string]map[string]any {
	return map[string]map[string]any{
		"config": {"memory": map[string]any{"backend": "local"}, "runtime": map[string]any{"timezone": timezone}},
		"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
		"grants": {"allow": map[string]any{
			"workspace": []any{"read", "write"},
			"tools":     []any{"list", "use"},
		}},
	}
}

// setupSpace creates the space, submits every layer, then validates and
// activates them: a layer validates against the pending docs of the
// layers not yet live, so submissions all land before the first validate.
func setupSpace(t *testing.T, revs *spawn.Revisions) {
	t.Helper()
	ctx := context.Background()
	if _, err := revs.CreateSpace(ctx, tenant, "alice", "dev"); err != nil {
		t.Fatalf("create space: %v", err)
	}
	layers := layerDocs()
	layers["user"] = userDocs("Europe/Berlin")
	ids := map[string]string{}
	for _, layer := range spawn.LayerOrder {
		docs, ok := layers[layer]
		if !ok {
			continue
		}
		rev, err := revs.Submit(ctx, tenant, "alice", layer, docs)
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		ids[layer] = rev.ID
	}
	for _, layer := range spawn.LayerOrder {
		id, ok := ids[layer]
		if !ok {
			continue
		}
		rev, err := revs.Validate(ctx, tenant, "alice", id)
		if err != nil {
			t.Fatalf("validate %s %s: %v", layer, id, err)
		}
		if rev.State != "validated" {
			t.Fatalf("validate %s %s: state %s detail %s", layer, id, rev.State, rev.Detail)
		}
		if _, err := revs.Activate(ctx, tenant, "alice", id); err != nil {
			t.Fatalf("activate %s %s: %v", layer, id, err)
		}
	}
}

func submitValidated(t *testing.T, revs *spawn.Revisions, layer string, docs map[string]map[string]any) spawn.Revision {
	t.Helper()
	ctx := context.Background()
	rev, err := revs.Submit(ctx, tenant, "alice", layer, docs)
	if err != nil {
		t.Fatalf("submit %s: %v", layer, err)
	}
	rev, err = revs.Validate(ctx, tenant, "alice", rev.ID)
	if err != nil {
		t.Fatalf("validate %s %s: %v", layer, rev.ID, err)
	}
	if rev.State != "validated" {
		t.Fatalf("validate %s %s: state %s detail %s", layer, rev.ID, rev.State, rev.Detail)
	}
	return rev
}

func effectiveDigest(t *testing.T, revs *spawn.Revisions) string {
	t.Helper()
	spec, err := revs.Effective(context.Background(), tenant, "alice")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	return spec.Digest
}

func unmarshalDoc(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }

func revisionState(t *testing.T, revs *spawn.Revisions, id string) string {
	t.Helper()
	list, err := revs.ListRevisions(context.Background(), tenant, "alice", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rev := range list {
		if rev.ID == id {
			return rev.State
		}
	}
	t.Fatalf("revision %s not listed", id)
	return ""
}

// Rollback is an activate on a superseded revision: the live pointer chain
// re-anchors the current active to the rollback target.
func TestRevisionRollbackActivatesOlder(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		first := effectiveDigest(t, revs)

		second := submitValidated(t, revs, "user", userDocs("UTC"))
		if _, err := revs.Activate(ctx, tenant, "alice", second.ID); err != nil {
			t.Fatalf("activate second: %v", err)
		}
		if got := effectiveDigest(t, revs); got == first {
			t.Fatalf("second activation kept digest %s", got)
		}

		// Roll back to the superseded user revision.
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var older string
		for _, rev := range list {
			if rev.State == "superseded" {
				older = rev.ID
			}
		}
		if older == "" {
			t.Fatal("no superseded user revision")
		}
		if _, err := revs.Activate(ctx, tenant, "alice", older); err != nil {
			t.Fatalf("rollback activate: %v", err)
		}
		if got := effectiveDigest(t, revs); got != first {
			t.Fatalf("rollback digest = %s, want the original %s", got, first)
		}
		if state := revisionState(t, revs, older); state != "active" {
			t.Fatalf("rolled-back revision is %s", state)
		}
		if state := revisionState(t, revs, second.ID); state != "superseded" {
			t.Fatalf("forward revision is %s", state)
		}
	})
}

// A crash inside the activation transaction — after the state rows moved,
// before commit — rolls the whole cut back: the previous active keeps
// serving and the effective digest does not move.
func TestApplyCrashLeavesPreviousActive(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		first := effectiveDigest(t, revs)

		second := submitValidated(t, revs, "user", userDocs("UTC"))
		revs.BeforeCommit = func() error { return errors.New("injected crash") }
		if _, err := revs.Activate(ctx, tenant, "alice", second.ID); err == nil {
			t.Fatal("activate with injected crash succeeded")
		}
		revs.BeforeCommit = nil

		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		active := 0
		for _, rev := range list {
			if rev.State == "active" && rev.ID != second.ID {
				active++
			}
			if rev.State == "active" && rev.ID == second.ID {
				t.Fatal("crashed activation left the target active")
			}
		}
		if active != 1 {
			t.Fatalf("want exactly one active (the previous), got %d", active)
		}
		if got := effectiveDigest(t, revs); got != first {
			t.Fatalf("crash moved the effective digest to %s", got)
		}

		// The same revision activates cleanly once the crash is gone.
		if _, err := revs.Activate(ctx, tenant, "alice", second.ID); err != nil {
			t.Fatalf("activate after crash cleared: %v", err)
		}
	})
}

// The effective spec is the intersection of the layers: the agent layer
// drops a toolset and a grant operation the user layer admitted, and
// widening past the user layer is refused at validate.
func TestEffectiveSpecIntersectsLayers(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()

		narrowed := submitValidated(t, revs, "agent", map[string]map[string]any{
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}}},
			"grants": {"allow": map[string]any{"workspace": []any{"read"}, "tools": []any{"list", "use"}}},
		})
		if _, err := revs.Activate(ctx, tenant, "alice", narrowed.ID); err != nil {
			t.Fatalf("activate agent layer: %v", err)
		}
		spec, err := revs.Effective(ctx, tenant, "alice")
		if err != nil {
			t.Fatalf("effective: %v", err)
		}
		var toolsDoc struct {
			Core map[string]any `json:"core"`
		}
		if err := unmarshalDoc(spec.Documents["tools"], &toolsDoc); err != nil {
			t.Fatalf("tools doc: %v", err)
		}
		if _, ok := toolsDoc.Core["files"]; ok {
			t.Error("agent-dropped toolset files still in the effective spec")
		}
		if _, ok := toolsDoc.Core["native:file"]; !ok {
			t.Error("native:file missing from the effective spec")
		}
		var grantsDoc struct {
			Allow map[string][]string `json:"allow"`
		}
		if err := unmarshalDoc(spec.Documents["grants"], &grantsDoc); err != nil {
			t.Fatalf("grants doc: %v", err)
		}
		if got := grantsDoc.Allow["workspace"]; len(got) != 1 || got[0] != "read" {
			t.Errorf("agent grant narrowing lost: workspace ops = %v", got)
		}

		// Widening past the user layer is rejected, not merged: the agent
		// names a toolset no layer above admitted.
		widened, err := revs.Submit(ctx, tenant, "alice", "agent", map[string]map[string]any{
			"tools": {"core": map[string]any{
				"native:file": map[string]any{"via": "native"},
				"search":      map[string]any{"via": "toolhub"},
			}},
		})
		if err != nil {
			t.Fatalf("submit widening agent: %v", err)
		}
		widened, err = revs.Validate(ctx, tenant, "alice", widened.ID)
		if err != nil {
			t.Fatalf("validate widening agent: %v", err)
		}
		if widened.State != "rejected" {
			t.Fatalf("widening agent layer is %s, want rejected", widened.State)
		}
		if widened.Error == "" || widened.Detail == "" {
			t.Error("rejected revision carries no error key or detail")
		}
	})
}

// Concurrent activations of the same layer: exactly one wins the CAS —
// the loser gets a conflict and the layer never holds two active rows.
func TestAtMostOneActivePerLayer(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()

		a := submitValidated(t, revs, "user", userDocs("UTC"))
		b := submitValidated(t, revs, "user", userDocs("Asia/Tokyo"))
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = revs.Activate(ctx, tenant, "alice", a.ID) }()
		go func() { defer wg.Done(); _, errs[1] = revs.Activate(ctx, tenant, "alice", b.ID) }()
		wg.Wait()

		won, lost := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, store.ErrConflict):
				lost++
			default:
				t.Fatalf("activation failed with %v, want conflict or nil", err)
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("want one winner one conflict, got won=%d lost=%d", won, lost)
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		active := 0
		for _, rev := range list {
			if rev.State == "active" {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("user layer holds %d active rows", active)
		}
	})
}

// Activations past the keep slack move the oldest superseded revisions to
// the final pruned state; the newest stay superseded and rollback-ready.
func TestPruneKeepsNewestSuperseded(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		zones := []string{"UTC", "Asia/Tokyo", "America/New_York", "Europe/London", "Australia/Sydney"}
		for _, zone := range zones {
			rev := submitValidated(t, revs, "user", userDocs(zone))
			if _, err := revs.Activate(ctx, tenant, "alice", rev.ID); err != nil {
				t.Fatalf("activate %s: %v", zone, err)
			}
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		states := map[string]int{}
		for _, rev := range list {
			states[rev.State]++
		}
		if states["active"] != 1 {
			t.Fatalf("active = %d, want 1", states["active"])
		}
		if states["superseded"] != 3 {
			t.Fatalf("superseded = %d, want the 3 newest kept", states["superseded"])
		}
		if states["pruned"] != 2 {
			t.Fatalf("pruned = %d, want the 2 oldest moved on", states["pruned"])
		}
		// The oldest superseded rows are the pruned ones: list order is
		// newest-first, the pruned tail is the original + first update.
		var prunedIDs []string
		for _, rev := range list {
			if rev.State == "pruned" {
				prunedIDs = append(prunedIDs, rev.ID)
			}
		}
		if len(prunedIDs) != 2 || prunedIDs[1] != list[len(list)-1].ID {
			t.Fatalf("pruned set %v is not the oldest tail", prunedIDs)
		}
	})
}
