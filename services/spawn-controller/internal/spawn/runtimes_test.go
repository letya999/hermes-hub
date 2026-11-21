package spawn_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

// testClock is the shared manual clock sweeps and services advance
// through — deadlines move only when the test says so.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeBackend is a real in-package Backend: the lifecycle under test is
// spawn's own rows and transitions; the backend only has to remember what
// a daemon would remember — which workloads exist, and which calls were
// refused or left half-done.
type fakeBackend struct {
	mu       sync.Mutex
	live     map[string]backend.Runtime
	specs    map[string]backend.Spec
	starts   int
	startErr error
	listErr  error
	stuck    map[string]bool // Stop is accepted but the workload stays — an unconfirmed stop
	gate     chan struct{}   // when set, the first Start waits for the test to close it
	entered  chan struct{}   // closed when the first Start is entered
	exec     execFunc        // Exec override; nil answers a clean result
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{live: map[string]backend.Runtime{}, specs: map[string]backend.Spec{}, stuck: map[string]bool{}, entered: make(chan struct{})}
}

func (f *fakeBackend) Start(ctx context.Context, spec backend.Spec) (backend.Runtime, error) {
	select {
	case <-f.entered:
	default:
		close(f.entered)
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return backend.Runtime{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return backend.Runtime{}, f.startErr
	}
	f.starts++
	rt := backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Address: "fake://" + spec.Key}
	f.live[spec.Key] = rt
	f.specs[spec.Key] = spec
	return rt, nil
}

func (f *fakeBackend) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stuck[id] {
		return nil
	}
	delete(f.live, id)
	return nil
}

func (f *fakeBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.live[id]
	if !ok {
		return backend.Runtime{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	return rt, nil
}

func (f *fakeBackend) List(context.Context) ([]backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]backend.Runtime, 0, len(f.live))
	for _, rt := range f.live {
		out = append(out, rt)
	}
	return out, nil
}

func (f *fakeBackend) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeBackend) specOf(key string) backend.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[key]
}

func (f *fakeBackend) evict(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, id)
}

func (f *fakeBackend) plant(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[id] = backend.Runtime{ID: id, State: "running"}
}

// execFunc answers Exec calls when set; the default succeeds silently so
// the canary/drift probes a claim runs get their clean bill.
type execFunc func(backend.ExecSpec) (backend.ExecResult, error)

func (f *fakeBackend) Exec(_ context.Context, id string, spec backend.ExecSpec) (backend.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return backend.ExecResult{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	if f.exec != nil {
		return f.exec(spec)
	}
	return backend.ExecResult{}, nil
}

// VerifyWorker answers the reuse guard's profile re-check: the workload
// must still be live, and a spec different from the one Start recorded is
// a refusal — the worker's isolation claims came from that spec.
func (f *fakeBackend) VerifyWorker(_ context.Context, id string, spec backend.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	got := f.specs[id]
	if got.Key != spec.Key || got.Image != spec.Image || len(got.Mounts) != len(spec.Mounts) {
		return fmt.Errorf("fake backend: worker profile drifted")
	}
	return nil
}

func (f *fakeBackend) alive(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.live[id]
	return ok
}

// runtimeFixture wires the durable services over one store and one
// manual clock: Revisions for the space bootstrap, Runtimes for claims,
// Sweeper for deadline and reconcile passes.
type runtimeFixture struct {
	store    *store.Store
	revs     *spawn.Revisions
	runtimes *spawn.Runtimes
	sweeper  *spawn.Sweeper
	clock    *testClock
	backend  *fakeBackend
}

func newRuntimeFixture(t *testing.T, url string, b *fakeBackend) *runtimeFixture {
	t.Helper()
	st := storetest.Open(t, url, migrations.FS)
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	revs := testRevisionsOn(t, st, clock)
	rts := &spawn.Runtimes{Store: st, Backend: b, Image: "harness-runtime", Now: clock.now}
	return &runtimeFixture{
		store: st, revs: revs, runtimes: rts, clock: clock, backend: b,
		sweeper: &spawn.Sweeper{Store: st, Backend: b, Now: clock.now},
	}
}

// testRevisionsOn is testRevisions with the fixture's clock instead of a
// self-advancing one — lifecycle tests move time explicitly.
func testRevisionsOn(t *testing.T, st *store.Store, clock *testClock) *spawn.Revisions {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatalf("catalog generate: %v", err)
	}
	return &spawn.Revisions{Store: st, Catalog: docs, Now: clock.now}
}

// setupSpaceAs is setupSpace under an explicit tenant: the second tenant
// of the cross-tenant test bootstraps the same space id with the same
// documents, so even an identical effective digest can never join the
// two tenants' pools.
func setupSpaceAs(t *testing.T, revs *spawn.Revisions, tn store.Tenant) {
	t.Helper()
	ctx := context.Background()
	if _, err := revs.CreateSpace(ctx, tn, "alice", "dev"); err != nil {
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

func runtimeStateAs(t *testing.T, st *store.Store, tn store.Tenant, id string) string {
	t.Helper()
	var state string
	err := st.ReadTx(context.Background(), func(tx *store.Tx) error {
		return tx.Scoped(tn).QueryRow("SELECT state FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&state)
	})
	if err != nil {
		t.Fatalf("runtime state %s: %v", id, err)
	}
	return state
}

func runtimeDeadline(t *testing.T, st *store.Store, tn store.Tenant, id string) int64 {
	t.Helper()
	var due *int64
	if err := st.ReadTx(context.Background(), func(tx *store.Tx) error {
		return tx.Scoped(tn).QueryRow("SELECT deadline FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&due)
	}); err != nil {
		t.Fatalf("runtime deadline %s: %v", id, err)
	}
	if due == nil {
		t.Fatalf("runtime %s carries no deadline", id)
	}
	return *due
}

func leaseCountAs(t *testing.T, st *store.Store, tn store.Tenant, runtimeID string) int {
	t.Helper()
	var n int
	err := st.ReadTx(context.Background(), func(tx *store.Tx) error {
		var err error
		n, err = spawn.LeaseCount(tx.Scoped(tn), runtimeID)
		return err
	})
	if err != nil {
		t.Fatalf("lease count %s: %v", runtimeID, err)
	}
	return n
}

// insertRuntimeRow writes one runtime row in the given state with an
// explicit deadline — the shape a claim leaves behind, used to plant
// rows the normal path no longer produces (a stale-digest warm row is
// a pre-fix or pre-crash leftover).
func insertRuntimeRow(t *testing.T, st *store.Store, tn store.Tenant, id, spaceID, agent string, gen int64, state, digest string, deadline time.Time) {
	t.Helper()
	err := st.Tx(context.Background(), func(tx *store.Tx) error {
		q := tx.Scoped(tn)
		if err := q.Insert("runtimes", []string{"id", "space_id", "agent", "generation", "state", "deadline", "policy_digest", "image_digest", "backend_id"}, id, spaceID, agent, gen, state, deadline.Unix(), digest, "harness-runtime", id); err != nil {
			return err
		}
		return q.Insert("runtimes_history", []string{"id", "seq", "at", "from_state", "event", "to_state"}, id, 1, deadline.Unix(), "", "create", state)
	})
	if err != nil {
		t.Fatalf("insert %s row: %v", state, err)
	}
}

// insertStartingRow writes one runtime row in starting with an explicit
// deadline — the shape a claim leaves behind while its Start runs, used
// to test the start budget without racing a real claim.
func insertStartingRow(t *testing.T, st *store.Store, tn store.Tenant, id, spaceID, agent string, gen int64, deadline time.Time) {
	t.Helper()
	insertRuntimeRow(t, st, tn, id, spaceID, agent, gen, "starting", "sha256:test", deadline)
}

// A claim binds the tenant and both digests to the generation row it
// starts, and a second claim for the same key reuses the warm row — the
// backend is asked once.
func TestWarmClaimBindsTenantAndDigest(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		digest := effectiveDigest(t, fx.revs)
		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if rt.State != "warm" || rt.PolicyDigest != digest || rt.ImageDigest != "harness-runtime" || rt.Generation != 1 {
			t.Fatalf("claim row: %+v, want warm gen1 bound to %s", rt, digest)
		}
		spec := fx.backend.specOf(rt.BackendID)
		if spec.Org != tenant.Org || spec.Owner != tenant.Owner || spec.Space != "alice" || spec.Agent != "main" || spec.Image != "harness-runtime" {
			t.Fatalf("backend spec not bound to tenant: %+v", spec)
		}
		again, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("second claim: %v", err)
		}
		if again.ID != rt.ID {
			t.Fatalf("warm reuse returned %s, want %s", again.ID, rt.ID)
		}
		if n := fx.backend.startCount(); n != 1 {
			t.Fatalf("backend started %d runtimes, want 1", n)
		}
	})
}

// Another tenant's claim for the same space id and agent — under the
// same image identity and even the same policy digest — must never see
// or reuse the first tenant's warm row.
func TestWarmRuntimeNeverCrossesTenants(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		other := storetest.Colleague // same org, different owner
		setupSpaceAs(t, fx.revs, other)
		ctx := context.Background()

		mine, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		theirs, err := fx.runtimes.Claim(ctx, other, "alice", "main")
		if err != nil {
			t.Fatalf("other tenant claim: %v", err)
		}
		if theirs.ID == mine.ID {
			t.Fatalf("cross-tenant reuse: %s serves two owners", theirs.ID)
		}
		if n := fx.backend.startCount(); n != 2 {
			t.Fatalf("backend started %d runtimes, want one per tenant", n)
		}
		spec := fx.backend.specOf(theirs.BackendID)
		if spec.Owner != other.Owner || spec.Org != other.Org {
			t.Fatalf("other tenant's spec bound to %+v", spec)
		}
		if got := runtimeStateAs(t, fx.store, tenant, mine.ID); got != "warm" {
			t.Fatalf("my runtime is %s after the foreign claim", got)
		}
	})
}

// Activation drains every warm or busy runtime bound to the old digest in
// the activation transaction: the next claim starts a new generation on
// the new policy, and the drained row's workload is stopped to absent.
func TestRevokeDrainsOldDigest(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		old, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, old.ID); got != "draining" {
			t.Fatalf("old-digest runtime is %s, want draining", got)
		}
		next, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim after revoke: %v", err)
		}
		if next.ID == old.ID || next.PolicyDigest == old.PolicyDigest {
			t.Fatalf("claim after revoke reused old binding: %+v", next)
		}
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if fx.backend.alive(old.BackendID) {
			t.Fatal("drained workload survived the sweep")
		}
		if got := runtimeStateAs(t, fx.store, tenant, old.ID); got != "absent" {
			t.Fatalf("drained runtime is %s, want absent", got)
		}
	})
}

// A runtime whose Start outlives its policy's activation must never
// serve the revoked digest: activation drains only warm/busy rows, so
// the ready check is the line — the row warms into the same transaction
// that drains it, the claim re-plans for the current digest, and the
// stale workload is stopped on the spot.
func TestStartingRuntimeUnderRevokedDigestNeverServes(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fx.backend.gate = make(chan struct{})

		claimed := make(chan spawn.Runtime, 1)
		claimErr := make(chan error, 1)
		go func() {
			rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
			if err != nil {
				claimErr <- err
				return
			}
			claimed <- rt
		}()
		select {
		case <-fx.backend.entered:
		case <-ctx.Done():
			t.Fatal("claim never reached the backend")
		}
		// The plan committed the starting row before Start was called.
		var stale string
		if err := fx.store.ReadTx(ctx, func(tx *store.Tx) error {
			return tx.Scoped(tenant).QueryRow("SELECT id FROM runtimes WHERE {tenant} AND state = 'starting'").Scan(&stale)
		}); err != nil {
			t.Fatalf("starting row: %v", err)
		}
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		newDigest := effectiveDigest(t, fx.revs)
		close(fx.backend.gate)

		var rt spawn.Runtime
		select {
		case err := <-claimErr:
			t.Fatalf("claim: %v", err)
		case rt = <-claimed:
		case <-ctx.Done():
			t.Fatal("claim never returned")
		}
		if rt.PolicyDigest != newDigest {
			t.Fatalf("claim under revoked digest returned %s bound to %s", rt.ID, rt.PolicyDigest)
		}
		if rt.ID == stale {
			t.Fatal("claim re-planned onto the revoked generation")
		}
		if rt.Generation != 2 {
			t.Fatalf("re-planned claim is generation %d, want 2", rt.Generation)
		}
		if got := runtimeStateAs(t, fx.store, tenant, stale); got != "draining" {
			t.Fatalf("revoked-digest runtime is %s, want draining", got)
		}
		if err := fx.runtimes.Acquire(ctx, tenant, stale, "ls-stale"); err == nil {
			t.Fatal("leased a runtime bound to a revoked digest")
		}
		if fx.backend.alive(stale) {
			t.Fatal("workload under revoked digest survived the drain")
		}
		if n := fx.backend.startCount(); n != 2 {
			t.Fatalf("backend started %d workloads, want stale + replanned", n)
		}
	})
}

// A warm row bound to another policy digest is never a claim's runtime:
// the reuse key is exact. The row itself is planted directly because the
// post-fix lifecycle no longer produces stale warm rows — it models a
// leftover from before a crash or an upgrade. The sweeper drains it as
// the backstop for the activation-time drain.
func TestWarmClaimRequiresMatchingDigest(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		oldDigest := effectiveDigest(t, fx.revs)
		if _, err := fx.runtimes.Claim(ctx, tenant, "alice", "main"); err != nil {
			t.Fatalf("claim: %v", err)
		}
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		newDigest := effectiveDigest(t, fx.revs)
		// A warm row on the revoked digest survives as a leftover; its
		// workload is up but refuses to die, so the sweep's drain is
		// visible as a stuck draining rather than a confirmed stop.
		insertRuntimeRow(t, fx.store, tenant, "rt-stale", "alice", "main", 5, "warm", oldDigest, fx.clock.now().Add(15*time.Minute))
		fx.backend.plant("rt-stale")
		fx.backend.stuck["rt-stale"] = true

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if rt.ID == "rt-stale" {
			t.Fatal("claim reused a warm row bound to a revoked digest")
		}
		if rt.PolicyDigest != newDigest || rt.Generation != 6 {
			t.Fatalf("claim row: %+v, want gen6 bound to %s", rt, newDigest)
		}
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, "rt-stale"); got != "draining" {
			t.Fatalf("stale-digest warm row survived the sweep as %s", got)
		}
	})
}

// A starting row whose start budget expired moves to failed on the sweep,
// and the half-started workload the row still names is stopped — the
// budget is a teardown, not just a state label.
func TestStartingDeadlineFails(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		ctx := context.Background()
		insertStartingRow(t, fx.store, tenant, "rt-dead", "alice", "main", 1, fx.clock.now().Add(-time.Second))
		fx.backend.plant("rt-dead") // the workload Start left half-up

		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, "rt-dead"); got != "failed" {
			t.Fatalf("expired starting row is %s, want failed", got)
		}
		if fx.backend.alive("rt-dead") {
			t.Fatal("half-started workload survived the start budget")
		}
	})
}

// A warm row whose workload vanished under it moves to missing on a
// successful backend inventory — and a busy row's leases go with it.
func TestInspectNothingMarksMissing(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := fx.runtimes.Acquire(ctx, tenant, rt.ID, "ls-1"); err != nil {
			t.Fatalf("acquire: %v", err)
		}
		fx.backend.evict(rt.BackendID) // the workload died under the row
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "missing" {
			t.Fatalf("absent workload's row is %s, want missing", got)
		}
		if n := leaseCountAs(t, fx.store, tenant, rt.ID); n != 0 {
			t.Fatalf("missing runtime holds %d leases", n)
		}
	})
}

// A daemon error is not an inventory: when List fails, no row may be
// marked missing — fail closed, reconcile nothing.
func TestDaemonErrorNeverMarksMissing(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		fx.backend.listErr = errors.New("daemon unreachable")
		fx.backend.evict(rt.BackendID)
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "warm" {
			t.Fatalf("daemon error moved the row to %s", got)
		}
	})
}

// A restarted controller recovers its world from the rows: the runtime a
// previous process claimed is still warm and claimable, no new workload
// is started, and the reconciler confirms the row against the backend.
func TestControllerRestartRecoversFromRows(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// Restart: same store, a fresh controller-side view of a backend
		// that still carries the workload.
		backend2 := newFakeBackend()
		backend2.plant(rt.BackendID)
		runtimes2 := &spawn.Runtimes{Store: fx.store, Backend: backend2, Image: "harness-runtime", Now: fx.clock.now}
		sweeper2 := &spawn.Sweeper{Store: fx.store, Backend: backend2, Now: fx.clock.now}

		if err := sweeper2.Tick(ctx); err != nil {
			t.Fatalf("boot sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "warm" {
			t.Fatalf("restart reconcile moved live row to %s", got)
		}
		again, err := runtimes2.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim after restart: %v", err)
		}
		if again.ID != rt.ID {
			t.Fatalf("restart claimed %s, want the recovered %s", again.ID, rt.ID)
		}
		if n := backend2.startCount(); n != 0 {
			t.Fatalf("restart spawned %d new workloads", n)
		}
	})
}

// The renew edge re-arms the busy hold bound: after a renewal the row is
// still busy and its deadline sits further out than the lease's first.
func TestLeaseRenewExtendsBusy(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := fx.runtimes.Acquire(ctx, tenant, rt.ID, "ls-1"); err != nil {
			t.Fatalf("acquire: %v", err)
		}
		first := runtimeDeadline(t, fx.store, tenant, rt.ID)
		fx.clock.add(time.Hour)
		if _, err := fx.runtimes.Renew(ctx, tenant, "ls-1"); err != nil {
			t.Fatalf("renew: %v", err)
		}
		second := runtimeDeadline(t, fx.store, tenant, rt.ID)
		if second <= first {
			t.Fatalf("renew did not re-arm: %d -> %d", first, second)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "busy" {
			t.Fatalf("renew moved the runtime to %s", got)
		}
		// A renewal past the bound is refused: the stuck hold drains on
		// the sweep (the workload refuses to die, so draining is visible)
		// and its lease is gone.
		fx.backend.stuck[rt.BackendID] = true
		fx.clock.add(3 * time.Hour)
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "draining" {
			t.Fatalf("expired hold is %s, want draining", got)
		}
		if _, err := fx.runtimes.Renew(ctx, tenant, "ls-1"); err == nil {
			t.Fatal("renewed a lease the drain already removed")
		}
	})
}

// Moving a runtime into draining deletes its lease rows in the same
// transaction — a drained generation can never carry a hold forward.
func TestDrainRemovesLeaseRows(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		for _, id := range []string{"ls-1", "ls-2"} {
			if err := fx.runtimes.Acquire(ctx, tenant, rt.ID, id); err != nil {
				t.Fatalf("acquire %s: %v", id, err)
			}
		}
		if n := leaseCountAs(t, fx.store, tenant, rt.ID); n != 2 {
			t.Fatalf("holds %d leases, want 2", n)
		}
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "draining" {
			t.Fatalf("drained runtime is %s", got)
		}
		if n := leaseCountAs(t, fx.store, tenant, rt.ID); n != 0 {
			t.Fatalf("drain left %d lease rows", n)
		}
	})
}

// Every start of a (space, agent) pair lands on the next generation —
// tombstones count, so a generation number is never issued twice.
func TestGenerationIncrementsPerStart(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		first, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		second, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("second claim: %v", err)
		}
		if first.Generation != 1 || second.Generation != 2 {
			t.Fatalf("generations %d, %d — want 1 then 2", first.Generation, second.Generation)
		}
		if second.ID == first.ID {
			t.Fatal("second generation reused the first's row id")
		}
	})
}

// Concurrent claims for one (tenant, space, agent) key start at most one
// backend workload — every claim resolves to the same generation row.
func TestConcurrentClaimsStartOnce(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Hold the first start until every claim has entered its plan, so
		// the losers really do meet a committed starting row.
		fx.backend.gate = make(chan struct{})
		const n = 8
		results := make([]spawn.Runtime, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = fx.runtimes.Claim(ctx, tenant, "alice", "main")
			}(i)
		}
		select {
		case <-fx.backend.entered:
		case <-ctx.Done():
			t.Fatal("no claim reached the backend")
		}
		time.Sleep(50 * time.Millisecond) // let the losers' plans commit or collide
		close(fx.backend.gate)
		wg.Wait()

		var first string
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				t.Fatalf("claim %d: %v", i, errs[i])
			}
			if first == "" {
				first = results[i].ID
			} else if results[i].ID != first {
				t.Fatalf("claim %d got %s, want the shared %s", i, results[i].ID, first)
			}
		}
		if got := fx.backend.startCount(); got != 1 {
			t.Fatalf("%d claims started %d workloads", n, got)
		}
	})
}

// A space with no stored effective digest refuses the claim with a
// localized unavailable — nothing unmanaged ever reaches the backend.
func TestClaimWithoutEffectiveSpecRefused(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		ctx := context.Background()
		if _, err := fx.revs.CreateSpace(ctx, tenant, "alice", "dev"); err != nil {
			t.Fatalf("create space: %v", err)
		}

		for _, spaceID := range []string{"alice", "ghost"} {
			if _, err := fx.runtimes.Claim(ctx, tenant, spaceID, "main"); !errors.Is(err, httpapi.ErrUnavailable) {
				t.Fatalf("claim %s: want unavailable, got %v", spaceID, err)
			}
		}
		if n := fx.backend.startCount(); n != 0 {
			t.Fatalf("refused claims started %d workloads", n)
		}
	})
}

// The release of a runtime's last lease moves it back to warm through the
// machine; an extra hold keeps it busy. Covered here because the message
// path relies on it — a forwarded call never leaves its runtime busy.
func TestReleaseReturnsToWarm(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		for _, id := range []string{"ls-1", "ls-2"} {
			if err := fx.runtimes.Acquire(ctx, tenant, rt.ID, id); err != nil {
				t.Fatalf("acquire %s: %v", id, err)
			}
		}
		if err := fx.runtimes.Release(ctx, tenant, rt.ID, "ls-1"); err != nil {
			t.Fatalf("release one: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "busy" {
			t.Fatalf("one hold released, runtime is %s", got)
		}
		if err := fx.runtimes.Release(ctx, tenant, rt.ID, "ls-2"); err != nil {
			t.Fatalf("release last: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "warm" {
			t.Fatalf("last hold released, runtime is %s", got)
		}
	})
}

// A draining row whose stop never confirms outlives its stop budget:
// the deadline quarantines it — "stopped" is only ever written on a
// confirmed stop.
func TestUnconfirmedStopQuarantines(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		fx.backend.stuck[rt.BackendID] = true
		rev := submitValidated(t, fx.revs, "user", userDocs("UTC"))
		if _, err := fx.revs.Activate(ctx, tenant, "alice", []string{rev.ID}, nil); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "draining" {
			t.Fatalf("unconfirmed stop moved the row to %s", got)
		}
		fx.clock.add(time.Minute) // past the 30s stop budget
		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("second sweep: %v", err)
		}
		if got := runtimeStateAs(t, fx.store, tenant, rt.ID); got != "quarantined" {
			t.Fatalf("unconfirmed drain is %s, want quarantined", got)
		}
	})
}

// A labeled workload the store never heard of is an orphan from a
// crashed controller: the reconciler stops it instead of leaving
// unmanaged compute running.
func TestOrphanWorkloadStopped(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		ctx := context.Background()
		fx.backend.plant("rt-orphan")

		if err := fx.sweeper.Tick(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if fx.backend.alive("rt-orphan") {
			t.Fatal("orphan workload survived reconciliation")
		}
	})
}

// The renew edge itself is forbidden outside busy — renewing a warm
// runtime is a conflict, and a missing lease is not_found.
func TestRenewRefusedOutsideBusy(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()

		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := fx.runtimes.Acquire(ctx, tenant, rt.ID, "ls-1"); err != nil {
			t.Fatalf("acquire: %v", err)
		}
		if err := fx.runtimes.Release(ctx, tenant, rt.ID, "ls-1"); err != nil {
			t.Fatalf("release: %v", err)
		}
		if _, err := fx.runtimes.Renew(ctx, tenant, "ls-1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("renew released lease: want not_found, got %v", err)
		}
	})
}

// Guard: a forbidden machine transition surfaces as a store conflict, so
// callers map it to conflict instead of mistaking it for unavailability.
func TestForbiddenIsConflict(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		fx := newRuntimeFixture(t, url, newFakeBackend())
		setupSpaceAs(t, fx.revs, tenant)
		ctx := context.Background()
		rt, err := fx.runtimes.Claim(ctx, tenant, "alice", "main")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		err = fx.store.Tx(ctx, func(tx *store.Tx) error {
			return fsm.Transition(tx.Scoped(tenant), spawn.RuntimeMachine, rt.ID, "renew", fx.clock.now())
		})
		if !errors.Is(err, fsm.ErrForbiddenTransition) || !errors.Is(err, store.ErrConflict) {
			t.Fatalf("forbidden renew on warm: want forbidden+conflict, got %v", err)
		}
	})
}
