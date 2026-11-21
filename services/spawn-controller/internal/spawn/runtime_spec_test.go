package spawn_test

// Managed-runtime spec builder tests (CHG-0336 S7c): the claim's stored
// effective bundle becomes the same launch contract the v0 supervisor's
// managedRunArgs produced — same mounts, same read-only flags, same
// pre-start materialization.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
	"gopkg.in/yaml.v3"
)

// managedFixture is runtimeFixture plus the space tree the spec builder
// mounts: the activation mirror publishes the generated revision, then the
// render produces the env/auth/config files the v0 supervisor wrote at
// apply time.
type managedFixture struct {
	*runtimeFixture
	root  string // the repo-shaped root holding config/ and spaces/
	dir   string // the space directory a claim materializes under
	rts   *spawn.Runtimes
	space string
}

func newManagedFixture(t *testing.T, url string) *managedFixture {
	t.Helper()
	root := t.TempDir()
	spacesRoot := filepath.Join(root, "spaces")
	dir := filepath.Join(spacesRoot, "alice")
	for _, sub := range []string{
		filepath.Join(root, "config"),
		filepath.Join(spacesRoot, "acme"),
		filepath.Join(dir, "skills"),
		filepath.Join(dir, "workspace"),
	} {
		if err := os.MkdirAll(sub, 0o770); err != nil {
			t.Fatalf("fixture dir: %v", err)
		}
	}
	writeFixtureFile(t, filepath.Join(root, "config", "SOUL.md"), "# fixture soul\n")
	writeFixtureFile(t, filepath.Join(dir, "runtime.auth"), "HUB_RUNTIME_AUTH=fixture-runtime-auth\n")
	writeFixtureFile(t, filepath.Join(dir, "secrets.dev.env"), "")

	st := storetest.Open(t, url, migrations.FS)
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	revs := testRevisionsOn(t, st, clock)
	revs.SpacesRoot = spacesRoot
	b := newFakeBackend()
	rts := &spawn.Runtimes{
		Store: st, Backend: b, Revisions: revs, Image: "harness-runtime",
		SpacesRoot: spacesRoot, RuntimePort: 8080, Now: clock.now,
	}
	fx := &managedFixture{
		runtimeFixture: &runtimeFixture{
			store: st, revs: revs, runtimes: rts, clock: clock, backend: b,
			sweeper: &spawn.Sweeper{Store: st, Backend: b, Now: clock.now},
		},
		root: root, dir: dir, rts: rts, space: "alice",
	}
	// The fixture's effective set is the shared layerDocs plus the tools
	// binding the managed read requires: a reviewed capability profile id
	// and generation.
	layers := layerDocs()
	// A non-org-scoped fixture keeps the spec builder off the organization
	// secrets path the org layer would demand on disk.
	stackDoc := layers["platform-ceiling"]["stack"]
	stackDoc["identity"] = map[string]any{"user": "alice", "organization": ""}
	user := userDocs("Europe/Berlin")
	user["tools"] = map[string]any{"binding": map[string]any{"profile_id": "fixture-profile", "generation": 1}}
	setupSpaceDocs(t, revs, tenant, layers, user)
	// The render contract's outputs the spec builder consumes: the runtime
	// env file with its reviewed auth, and the effective-hermes source —
	// marshalled through the same stack.Config the real render uses.
	writeFixtureFile(t, filepath.Join(dir, "managed-runtime.dev.env"), "HUB_RUNTIME_AUTH=fixture-runtime-auth\n")
	settings, err := stack.ReadEffective(dir, "dev")
	if err != nil {
		t.Fatalf("effective settings: %v", err)
	}
	body, err := yaml.Marshal(stack.Config(settings))
	if err != nil {
		t.Fatalf("marshal hermes config: %v", err)
	}
	writeFixtureFile(t, filepath.Join(dir, "generated", "hermes.dev.yaml"), string(body))
	return fx
}

// setupSpaceDocs is setupSpaceAs with the fixture's own layer documents.
func setupSpaceDocs(t *testing.T, revs *spawn.Revisions, tn store.Tenant, layers map[string]map[string]map[string]any, user map[string]map[string]any) {
	t.Helper()
	ctx := context.Background()
	if _, err := revs.CreateSpace(ctx, tn, "alice", "dev"); err != nil {
		t.Fatalf("create space: %v", err)
	}
	layers["user"] = user
	ids := map[string]string{}
	for _, layer := range spawn.LayerOrder {
		docs, ok := layers[layer]
		if !ok {
			continue
		}
		rev, err := revs.Submit(ctx, tn, "alice", layer, docs)
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		ids[layer] = rev.ID
	}
	var all []string
	for _, layer := range spawn.LayerOrder {
		id, ok := ids[layer]
		if !ok {
			continue
		}
		if _, err := revs.Validate(ctx, tn, "alice", id); err != nil {
			t.Fatalf("validate %s %s: %v", layer, id, err)
		}
		all = append(all, id)
	}
	if _, err := revs.Activate(ctx, tn, "alice", all, nil); err != nil {
		t.Fatalf("activate set %v: %v", all, err)
	}
}

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		t.Fatalf("fixture file dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("fixture file %s: %v", path, err)
	}
}

// claimSpec claims the fixture's managed runtime and answers the spec the
// backend was asked to apply.
func (fx *managedFixture) claimSpec(t *testing.T) backend.Spec {
	t.Helper()
	rt, err := fx.rts.ClaimManaged(context.Background(), tenant, fx.space, spawn.ClaimRequest{
		Agent: "main", RuntimeID: "rt-alice", UserID: "alice", OrganizationID: "acme",
		PolicyVersion: "v1", IdentityRevision: "irev-1",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	spec := fx.backend.specOf(rt.BackendID)
	if spec.Image == "" {
		t.Fatalf("claim produced an imageless spec: %+v", spec)
	}
	return spec
}

// mount is the comparable shape the golden table checks: (target, ro) —
// the v0 argv carried exactly this pair per --mount.
type mount struct {
	target string
	ro     bool
}

func specMounts(spec backend.Spec) []mount {
	out := make([]mount, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		out = append(out, mount{m.Target, m.ReadOnly})
	}
	return out
}

// TestManagedRuntimeSpecMatchesV0Mounts pins the v0 container contract:
// the claim's spec mounts exactly what managedRunArgs put under docker
// run — same targets, same read-only flags, nothing caller-supplied.
func TestManagedRuntimeSpecMatchesV0Mounts(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newManagedFixture(t, url)
		spec := fx.claimSpec(t)
		want := []mount{
			{"/state", false},
			{"/state/hermes", false},
			{"/state/home", false},
			{"/state/cache", false},
		}
		// The workspace passport's MountPlan carries /config, /workspace,
		// /skills and the read-only sub-binds — compare by membership so
		// the table stays readable across plan-owned detail.
		got := map[string]bool{}
		for _, m := range specMounts(spec) {
			got[m.target] = m.ro
		}
		for _, w := range want {
			ro, ok := got[w.target]
			if !ok || ro != w.ro {
				t.Fatalf("spec missing v0 mount %s ro=%v (mounts: %+v)", w.target, w.ro, specMounts(spec))
			}
		}
		for _, v0 := range []struct {
			target string
			ro     bool
		}{
			{"/skills-platform", true},
			{"/skills-effective-rw", false},
			{"/skills-meta", true},
			{"/skills-store", false},
			{"/state/hermes/config.yaml", true},
		} {
			ro, ok := got[v0.target]
			if !ok || ro != v0.ro {
				t.Fatalf("spec mount %s: got ro=%v present=%v, want ro=%v", v0.target, ro, ok, v0.ro)
			}
		}
		// The claim carries its routing identity into the workload env.
		env := map[string]bool{}
		for _, e := range spec.Env {
			env[e] = true
		}
		for _, want := range []string{
			"HUB_RUNTIME_ID=rt-alice", "HUB_USER_ID=alice",
			"HUB_ORGANIZATION_ID=acme", "HUB_POLICY_VERSION=v1",
			"HUB_STATE=/state", "HUB_WORKSPACE=/workspace",
		} {
			if !env[want] {
				t.Fatalf("spec env missing %q", want)
			}
		}
		if spec.Relay == nil || spec.Port != 8080 {
			t.Fatalf("managed spec carries no control relay/port: %+v", spec.Relay)
		}
		if len(spec.Networks) != 1 || spec.Networks[0] != "hermes-hub-agent-alice-dev" {
			t.Fatalf("managed networks: %v", spec.Networks)
		}
	})
}

// TestHermesConfigMaterializedAtSpawn: the effective Hermes config is
// rendered before the backend is asked, and the started workload mounts
// it read-only at the v0 path.
func TestHermesConfigMaterializedAtSpawn(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newManagedFixture(t, url)
		spec := fx.claimSpec(t)
		dest := filepath.Join(fx.dir, "generated", "hermes-effective.dev.yaml")
		info, err := os.Stat(dest)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("effective hermes config not materialized: %v", err)
		}
		var found bool
		for _, m := range spec.Mounts {
			if m.Target == "/state/hermes/config.yaml" && m.ReadOnly && m.Source == thaBind(dest) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no read-only config.yaml mount for %s (mounts: %+v)", dest, spec.Mounts)
		}
	})
}

// thaBind mirrors the backend's bind-source translation the spec builder
// applies — on a plain host path it is the identity.
func thaBind(path string) string { return path }

// TestToolPolicySnapshotAtSpawn: the executor's governance snapshot is
// materialized before start.
func TestToolPolicySnapshotAtSpawn(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newManagedFixture(t, url)
		fx.claimSpec(t)
		if _, err := os.Stat(filepath.Join(fx.dir, "generated", "tool-policy.dev.json")); err != nil {
			t.Fatalf("tool policy snapshot not materialized: %v", err)
		}
	})
}

// TestSkillsAndPluginsMaterializedAtSpawn: the effective skills view and
// the hub-plugins copy exist before the workload starts, and the spec
// mounts the skills contract.
func TestSkillsAndPluginsMaterializedAtSpawn(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newManagedFixture(t, url)
		spec := fx.claimSpec(t)
		for _, dir := range []string{
			filepath.Join(fx.dir, "generated", "skills"),
			filepath.Join(fx.dir, "generated", "plugins"),
		} {
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("materialized dir %s missing: %v", dir, err)
			}
		}
		var platform bool
		for _, m := range spec.Mounts {
			if m.Target == "/skills-platform" && m.ReadOnly {
				platform = true
			}
		}
		if !platform {
			t.Fatalf("spec mounts no read-only /skills-platform")
		}
	})
}

// TestRespawnDrainsOldGenerationOnDigestChange: a claim under a rotated
// policy reuses the activation-time drain machinery — the old generation
// is already draining before the new claim plans, and the sweeper stops
// its workload. The machinery is asserted end-to-end, never reimplemented
// per claim.
func TestRespawnDrainsOldGenerationOnDigestChange(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newManagedFixture(t, url)
		ctx := context.Background()
		req := spawn.ClaimRequest{Agent: "main", RuntimeID: "rt-alice"}
		old, err := fx.rts.ClaimManaged(ctx, tenant, fx.space, req)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// A new activation rotates the effective digest; the activation
		// transaction drains the live generation. The rotated docs keep
		// the fixture's tools binding so the new generation still carries
		// a reviewed capability profile.
		rotated := userDocs("UTC")
		rotated["tools"] = map[string]any{"binding": map[string]any{"profile_id": "fixture-profile", "generation": 2}}
		rev := submitValidated(t, fx.revs, "user", rotated)
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, old.ID); got != "draining" {
			t.Fatalf("old generation is %s after digest change, want draining", got)
		}
		next, err := fx.rts.ClaimManaged(ctx, tenant, fx.space, req)
		if err != nil {
			t.Fatalf("claim after digest change: %v", err)
		}
		if next.ID == old.ID || next.Generation != old.Generation+1 {
			t.Fatalf("claim after digest change: %+v, want gen%d", next, old.Generation+1)
		}
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if fx.backend.alive(old.BackendID) {
			t.Fatal("old generation's workload survived the drain")
		}
	})
}

// TestFirecrackerBackendCompilesNotSelectable: the microVM backend
// compiles against the managed Spec shape but refuses every call.
func TestFirecrackerBackendCompilesNotSelectable(t *testing.T) {
	var b backend.Backend = backend.Firecracker{}
	if _, err := b.Start(context.Background(), backend.Spec{Key: "k", Image: "i"}); err == nil {
		t.Fatal("firecracker Start did not refuse")
	}
	if err := b.Stop(context.Background(), "x"); err == nil {
		t.Fatal("firecracker Stop did not refuse")
	}
}

// TestKubernetesBackendCompilesNotSelectable: the pod backend compiles
// against the managed Spec shape but refuses every call.
func TestKubernetesBackendCompilesNotSelectable(t *testing.T) {
	var b backend.Backend = backend.Kubernetes{}
	if _, err := b.Start(context.Background(), backend.Spec{Key: "k", Image: "i"}); err == nil {
		t.Fatal("kubernetes Start did not refuse")
	}
	if err := b.Stop(context.Background(), "x"); err == nil {
		t.Fatal("kubernetes Stop did not refuse")
	}
}
