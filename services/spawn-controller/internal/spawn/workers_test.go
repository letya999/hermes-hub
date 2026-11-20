package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var workerTenant = store.Tenant{Org: "local", Owner: "alice"}

// workerFakeBackend records specs and answers the claim-time canary/proc
// probes and the reuse VerifyWorker check; its live set drives the
// sweeper's confirmed-stop transition.
type workerFakeBackend struct {
	mu     sync.Mutex
	live   map[string]backend.Runtime
	specs  map[string]backend.Spec
	execs  []backend.ExecSpec
	starts int
	exec   func(backend.ExecSpec) (backend.ExecResult, error)
}

func newWorkerFakeBackend() *workerFakeBackend {
	return &workerFakeBackend{live: map[string]backend.Runtime{}, specs: map[string]backend.Spec{}}
}

func (f *workerFakeBackend) Start(_ context.Context, spec backend.Spec) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	rt := backend.Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Name: "hermes-hub-rt-" + spec.Key}
	f.live[spec.Key] = rt
	f.specs[spec.Key] = spec
	return rt, nil
}

func (f *workerFakeBackend) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, id)
	return nil
}

func (f *workerFakeBackend) Inspect(_ context.Context, id string) (backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.live[id]
	if !ok {
		return backend.Runtime{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	return rt, nil
}

func (f *workerFakeBackend) List(context.Context) ([]backend.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backend.Runtime, 0, len(f.live))
	for _, rt := range f.live {
		out = append(out, rt)
	}
	return out, nil
}

func (f *workerFakeBackend) Exec(_ context.Context, id string, spec backend.ExecSpec) (backend.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return backend.ExecResult{}, fmt.Errorf("%w: %s", backend.ErrNotFound, id)
	}
	f.execs = append(f.execs, spec)
	if f.exec != nil {
		return f.exec(spec)
	}
	return backend.ExecResult{}, nil
}

// VerifyWorker refuses a workload that drifted from its start spec — the
// reuse path depends on this to quarantine dirty workers.
func (f *workerFakeBackend) VerifyWorker(_ context.Context, id string, spec backend.Spec) error {
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

func (f *workerFakeBackend) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *workerFakeBackend) specOf(key string) backend.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[key]
}

// workersFixture wires the durable worker service over a real store and the
// recording fake backend; cells are configured with a real state root and a
// shipped cellinit file so the spec builder runs end to end.
func workersFixture(t *testing.T, mutate func(*Cells)) (*store.Store, *workerFakeBackend, *Workers) {
	t.Helper()
	s := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	be := newWorkerFakeBackend()
	root := t.TempDir()
	cellinit := filepath.Join(root, "cellinit")
	if err := os.WriteFile(cellinit, []byte("cellinit"), 0o700); err != nil {
		t.Fatal(err)
	}
	cells := &Cells{
		ToolsImage:     "sha256:" + strings.Repeat("a", 64),
		CellInitSource: cellinit,
		StateRoot:      filepath.Join(root, "state"),
		SpacesRoot:     filepath.Join(root, "spaces"),
	}
	if err := os.MkdirAll(cells.StateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cells)
	}
	return s, be, &Workers{Store: s, Backend: be, Cells: cells}
}

func workerClaimReq() WorkerClaim {
	return WorkerClaim{
		CellKey: "binding|alice|bind-1|sha256:aaa", Image: "sha256:" + strings.Repeat("a", 64),
		Principal: "alice", BindingID: "bind-1",
		Limits: CellLimits{CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, TimeoutSeconds: 60, OutputBytes: 262144},
	}
}

// TestWorkerTransitionsWired drives the FSM through the service: claim →
// busy (leased), release → draining → absent once the backend confirms.
func TestWorkerTransitionsWired(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if lease.Worker.State != "busy" || lease.Lease == "" || !strings.HasPrefix(lease.Worker.ID, "wk-") {
		t.Fatalf("claim=%+v", lease)
	}
	if be.startCount() != 1 {
		t.Fatalf("starts=%d", be.startCount())
	}
	// Renew requires the lease.
	got, err := w.Renew(ctx, workerTenant, lease.Worker.ID, lease.Lease)
	if err != nil || got.State != "busy" {
		t.Fatalf("renew: %v %+v", err, got)
	}
	// Exec requires a held lease on a busy worker.
	res, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{Argv: []string{"true"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exec: %v %+v", err, res)
	}
	if _, err := w.Exec(ctx, workerTenant, lease.Worker.ID, "wl-wrong", backend.ExecSpec{Argv: []string{"true"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exec with foreign lease: %v, want not-found", err)
	}
	if err := w.Release(ctx, workerTenant, lease.Worker.ID, lease.Lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "absent" {
		t.Fatalf("released worker state=%q err=%v, want absent", row.State, err)
	}
	// Second claim on the same key is a fresh generation — the released
	// worker drained instead of being reused.
	lease2, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if lease2.Worker.ID == lease.Worker.ID || lease2.Worker.Generation != 2 {
		t.Fatalf("reclaim got %+v, want a new generation", lease2.Worker)
	}
}

func loadWorkerInStore(ctx context.Context, s *store.Store, t store.Tenant, id string) (Worker, error) {
	var out Worker
	err := s.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		out, err = loadWorker(tx.Scoped(t), id)
		return err
	})
	return out, err
}

// TestWorkerMountsOnlyOwnSpace: a claim may only bind the claiming space's
// own workspace — another space's tree, a path outside every root and a
// workspace belonging to a different principal all refuse before Start.
func TestWorkerMountsOnlyOwnSpace(t *testing.T) {
	_, be, w := workersFixture(t, nil)
	ctx := context.Background()
	// Plant alice's principal workspace under the spaces root.
	own := filepath.Join(w.Cells.SpacesRoot, "alice", "workspace")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(w.Cells.SpacesRoot, "bob", "workspace")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	req := workerClaimReq()
	req.Workspace = CellWorkspace{Scope: WorkspacePrincipal}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("own-space claim: %v", err)
	}
	mounts := be.specOf(lease.Worker.ID).Mounts
	found := false
	for _, m := range mounts {
		if m.Target == cellWorkGuest {
			found = true
			if filepath.Clean(m.Source) != filepath.Clean(own) {
				t.Fatalf("work mount=%q, want %q", m.Source, own)
			}
		}
	}
	if !found {
		t.Fatalf("principal workspace not mounted: %v", mounts)
	}
	// A claim whose principal is not the space refuses at spec construction —
	// cross-space mounts are impossible by construction. A fresh cell key
	// forces the build (an identical key would reuse the live worker, the
	// same way v0's warm key did).
	foreign := workerClaimReq()
	foreign.CellKey = "binding|bob|bind-9|sha256:aaa"
	foreign.Principal = "bob"
	foreign.Workspace = CellWorkspace{Scope: WorkspacePrincipal}
	if _, err := w.Claim(ctx, workerTenant, "alice", foreign); !errors.Is(err, httpapi.ErrForbidden) {
		t.Fatalf("foreign-principal claim: %v, want forbidden", err)
	}
	// Binding workspaces are controller-derived: a claim carrying any
	// host path — another space's binding dir, the state root itself or
	// an unrelated tree — is refused before Start, and a well-formed
	// claim mounts the derived per-space dir.
	for name, path := range map[string]string{
		"state root":   w.Cells.StateRoot,
		"other space":  filepath.Join(w.Cells.StateRoot, "bindings", "bob", "bind-1"),
		"unrelated":    t.TempDir(),
		"state escape": filepath.Join(w.Cells.StateRoot, "..", "outside"),
	} {
		bad := workerClaimReq()
		bad.CellKey = "binding|alice|bind-9|sha256:aaa"
		bad.Workspace = CellWorkspace{Scope: WorkspaceBinding, Path: path}
		if _, err := w.Claim(ctx, workerTenant, "alice", bad); !errors.Is(err, httpapi.ErrInvalid) {
			t.Fatalf("%s path claim: %v, want invalid", name, err)
		}
	}
	derived := workerClaimReq()
	derived.CellKey = "binding|alice|bind-9|sha256:aaa"
	derived.Workspace = CellWorkspace{Scope: WorkspaceBinding}
	leaseB, err := w.Claim(ctx, workerTenant, "alice", derived)
	if err != nil {
		t.Fatalf("derived binding claim: %v", err)
	}
	want := filepath.Join(w.Cells.StateRoot, "bindings", "alice", "bind-1")
	gotMount := ""
	for _, m := range be.specOf(leaseB.Worker.ID).Mounts {
		if m.Target == cellWorkGuest {
			gotMount = m.Source
		}
	}
	if filepath.Clean(gotMount) != filepath.Clean(want) {
		t.Fatalf("binding mount=%q, want derived %q", gotMount, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("derived workspace not materialized: %v", err)
	}
}

// TestWorkerNeverCrossesTenants: the same cell key and image under a
// different owner (or space) must mint a new worker — the tenant scope is
// part of the reuse key.
func TestWorkerNeverCrossesTenants(t *testing.T) {
	_, be, w := workersFixture(t, nil)
	ctx := context.Background()
	leaseA, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	other := store.Tenant{Org: "local", Owner: "bob"}
	req := workerClaimReq()
	req.Principal = "bob"
	leaseB, err := w.Claim(ctx, other, "bob", req)
	if err != nil {
		t.Fatal(err)
	}
	if leaseB.Worker.ID == leaseA.Worker.ID {
		t.Fatal("worker crossed tenants on an identical cell key")
	}
	if be.startCount() != 2 {
		t.Fatalf("cross-tenant reuse happened: starts=%d", be.startCount())
	}
	// Same tenant, different space: no reuse either.
	reqC := workerClaimReq()
	leaseC, err := w.Claim(ctx, workerTenant, "alice-2", reqC)
	if err != nil {
		t.Fatal(err)
	}
	if leaseC.Worker.ID == leaseA.Worker.ID {
		t.Fatal("worker crossed spaces on an identical cell key")
	}
}

// TestExecNoSecretInArgv: brokered credential values reach the cred
// sidecar through the exec file channel — and only there: the rules
// payload must carry the value (a shallow-copy strip would starve the
// sidecar) while spec argv/env, staged files and the persisted claim
// never do.
func TestExecNoSecretInArgv(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	secret := "sk-live-" + strings.Repeat("s", 16)
	req := workerClaimReq()
	req.Brokered = []CellCred{{EnvName: "GH_TOKEN", Host: "api.github.com", Prefix: "Bearer ", Value: secret}}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("brokered claim: %v", err)
	}
	spec := be.specOf(lease.Worker.ID)
	var blob strings.Builder
	_, _ = blob.WriteString(strings.Join(spec.Command, "\x00"))
	_, _ = blob.WriteString(strings.Join(spec.Env, "\x00"))
	viaExec := ""
	for _, sc := range spec.Sidecars {
		_, _ = blob.WriteString(strings.Join(sc.Command, "\x00"))
		_, _ = blob.WriteString(strings.Join(sc.Env, "\x00"))
		for _, f := range sc.Files {
			if f.ViaExec {
				viaExec += string(f.Content)
			} else {
				_, _ = blob.Write(f.Content) // docker-cp staged payload must be secret-free
			}
		}
	}
	if strings.Contains(blob.String(), secret) {
		t.Fatal("credential value reached spec argv/env/staged files")
	}
	if !strings.Contains(viaExec, secret) {
		t.Fatal("credential value never reached the cred sidecar's rules payload")
	}
	// The persisted claim column strips values.
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range row.claim.Brokered {
		if c.Value != "" {
			t.Fatal("credential value persisted on the worker row")
		}
	}
	if lease.CredHost == "" {
		t.Fatal("brokered claim returned no cred endpoint")
	}
}

// TestWorkerDifferentImageNewWorker: the cell key is (tenant, space, cell
// key, image) — same key with a second image must mint another worker,
// never reuse or collide with the first (M2).
func TestWorkerDifferentImageNewWorker(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	leaseA, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	reqB := workerClaimReq()
	reqB.Image = "sha256:" + strings.Repeat("b", 64)
	leaseB, err := w.Claim(ctx, workerTenant, "alice", reqB)
	if err != nil {
		t.Fatalf("second-image claim: %v", err)
	}
	if leaseB.Worker.ID == leaseA.Worker.ID {
		t.Fatal("second image collided with the first worker's row")
	}
	if be.startCount() != 2 {
		t.Fatalf("second image reused the first worker: starts=%d", be.startCount())
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, leaseA.Worker.ID)
	if err != nil || row.State != "busy" {
		t.Fatalf("first worker state=%q err=%v after second-image claim, want busy", row.State, err)
	}
}

// TestWorkerJoinBusyWithoutProbe: a claim on a key whose worker is held
// mid-exec joins as an additional holder — probing the cell reads a
// legitimately dirty state, so no probe may run while a live lease
// exists (the drain mutant dies here).
func TestWorkerJoinBusyWithoutProbe(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	// Any probe reads a busy cell as dirty: clean/procs fail.
	be.exec = func(spec backend.ExecSpec) (backend.ExecResult, error) {
		if len(spec.Argv) >= 2 && spec.Argv[0] == cellInitGuest && (spec.Argv[1] == "clean" || spec.Argv[1] == "procs") {
			return backend.ExecResult{ExitCode: 1}, nil
		}
		return backend.ExecResult{}, nil
	}
	leaseA, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	execsBefore := len(be.execs)
	leaseB, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("concurrent-key claim: %v", err)
	}
	if leaseB.Worker.ID != leaseA.Worker.ID || leaseB.Lease == leaseA.Lease {
		t.Fatalf("join got %+v, want same worker, fresh lease", leaseB)
	}
	for _, exec := range be.execs[execsBefore:] {
		if len(exec.Argv) >= 2 && exec.Argv[0] == cellInitGuest {
			t.Fatalf("probe exec ran against a held worker: %v", exec.Argv)
		}
	}
	// The original holder's lease still works — the claim did not drain
	// the worker under it.
	if _, err := w.Exec(ctx, workerTenant, leaseA.Worker.ID, leaseA.Lease, backend.ExecSpec{Argv: []string{"true"}}); err != nil {
		t.Fatalf("holder lease lost to the join: %v", err)
	}
	if err := w.Release(ctx, workerTenant, leaseA.Worker.ID, leaseA.Lease); err != nil {
		t.Fatal(err)
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, leaseA.Worker.ID)
	if err != nil || row.State != "busy" {
		t.Fatalf("worker state=%q after first release, want busy", row.State)
	}
	if err := w.Release(ctx, workerTenant, leaseB.Worker.ID, leaseB.Lease); err != nil {
		t.Fatal(err)
	}
	row, err = loadWorkerInStore(ctx, s, workerTenant, leaseA.Worker.ID)
	if err != nil || row.State != "absent" {
		t.Fatalf("worker state=%q after last release, want absent", row.State)
	}
}

// TestWorkerDirtyDrainWhenUnheld: an unleased busy row is the stale-hold
// shape — the probe runs on it and a dirty answer drains the generation,
// then the claim re-plans to a fresh worker.
func TestWorkerDirtyDrainWhenUnheld(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	be.exec = func(spec backend.ExecSpec) (backend.ExecResult, error) {
		if len(spec.Argv) >= 2 && spec.Argv[0] == cellInitGuest && (spec.Argv[1] == "clean" || spec.Argv[1] == "procs") {
			return backend.ExecResult{ExitCode: 1}, nil
		}
		return backend.ExecResult{}, nil
	}
	leaseA, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the crashed-holder shape directly: the busy row stands
	// with no lease row at all.
	if err := s.Tx(ctx, func(tx *store.Tx) error {
		_, err := tx.Scoped(workerTenant).Exec("DELETE FROM leases WHERE {tenant} AND runtime_id = ?", leaseA.Worker.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	leaseB, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatalf("re-claim after dirty drain: %v", err)
	}
	if leaseB.Worker.ID == leaseA.Worker.ID {
		t.Fatal("dirty worker was reused instead of drained")
	}
	if be.startCount() != 2 {
		t.Fatalf("dirty row did not re-plan: starts=%d", be.startCount())
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, leaseA.Worker.ID)
	if err != nil || (row.State != "draining" && row.State != "absent") {
		t.Fatalf("dirty worker state=%q, want draining", row.State)
	}
}

// TestWorkerLeaseExpiryDrainsStaleHold: a lease that outlives its expiry
// is swept — the hold disappears and a busy worker with none left drains
// (injected clock).
func TestWorkerLeaseExpiryDrainsStaleHold(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	cur := time.Unix(1_800_000_000, 0)
	w.Now = func() time.Time { return cur }
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	// Before expiry the hold is live and the sweep leaves it alone.
	if err := workerLeaseSweep(ctx, s, cur.Add(workerLeaseTTL-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{Argv: []string{"true"}}); err != nil {
		t.Fatalf("live lease lost early: %v", err)
	}
	// Past expiry the lease row is gone and the worker unleases.
	if err := workerLeaseSweep(ctx, s, cur.Add(workerLeaseTTL+time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "draining" {
		t.Fatalf("worker state=%q after lease expiry, want draining", row.State)
	}
	if _, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{Argv: []string{"true"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired lease still execs: %v, want not-found", err)
	}
	// The effects pass then confirms the stop: absent in the same tick.
	sweep := &Sweeper{Store: s, Backend: be, WorkerBackend: be, Now: func() time.Time { return cur }}
	if err := sweep.effects(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "absent" {
		t.Fatalf("worker state=%q after stop, want absent", row.State)
	}
	if len(be.live) != 0 {
		t.Fatalf("worker workload still live after expiry drain: %v", be.live)
	}
}

// TestWorkerAbsentRowsGC: an absent row (and its history) is retained for
// audit, then the retention deadline deletes both — the table does not
// grow per call forever.
func TestWorkerAbsentRowsGC(t *testing.T) {
	s, _, w := workersFixture(t, nil)
	ctx := context.Background()
	cur := time.Unix(1_800_000_000, 0)
	w.Now = func() time.Time { return cur }
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Release(ctx, workerTenant, lease.Worker.ID, lease.Lease); err != nil {
		t.Fatal(err)
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "absent" {
		t.Fatalf("released worker state=%q, want absent", row.State)
	}
	// Inside retention the row stays; past it the sweep deletes both
	// the row and its history.
	if err := workerSweep(ctx, s, cur.Add(workerAbsentRetention-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID); err != nil {
		t.Fatalf("retained row gone early: %v", err)
	}
	if err := workerSweep(ctx, s, cur.Add(workerAbsentRetention+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("absent row survived retention: %v", err)
	}
	var history int
	if err := s.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(workerTenant).QueryRow("SELECT count(*) FROM workers_history WHERE {tenant} AND id = ?", lease.Worker.ID).Scan(&history)
	}); err != nil {
		t.Fatal(err)
	}
	if history != 0 {
		t.Fatalf("absent row's history survived retention: %d rows", history)
	}
}

// TestWorkerOrphanPassSeesAllNonFinalRows: the orphan pass must know
// every non-final row — a busy worker beyond the old 1000-row cap is a
// live workload, never an orphan to stop.
func TestWorkerOrphanPassSeesAllNonFinalRows(t *testing.T) {
	s, be, w := workersFixture(t, nil)
	ctx := context.Background()
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	// Fill the table past the read cap with older non-final rows; their
	// deadlines sort first so the real busy worker is the row the cap
	// would drop.
	if err := s.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(workerTenant)
		for i := 0; i < 1100; i++ {
			id := "wk-plant-" + strings.Repeat("0", 4) + fmt.Sprint(i)
			err := q.Insert("workers", []string{"id", "space_id", "agent", "generation", "state", "deadline", "cell_key", "image_digest", "backend_id", "canary", "claim", "binding_id", "job_id"}, id, "alice", "agent", 1, "busy", 1, "key-"+fmt.Sprint(i), "img", "be-plant-"+id, "", "{}", "", "")
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtimeBE := newWorkerFakeBackend()
	sweep := &Sweeper{Store: s, Backend: runtimeBE, WorkerBackend: be}
	if err := sweep.reconcile(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	_, live := be.live[lease.Worker.BackendID]
	be.mu.Unlock()
	if !live {
		t.Fatal("busy worker beyond the read cap was stopped as an orphan")
	}
	row, err := loadWorkerInStore(ctx, s, workerTenant, lease.Worker.ID)
	if err != nil || row.State != "busy" {
		t.Fatalf("busy worker state=%q after reconcile, want busy", row.State)
	}
}

// TestWorkerExecEnvReserved: exec env names that would reconfigure the
// controller's docker CLI are refused server-side — the workload has no
// legitimate use for DOCKER_*.
func TestWorkerExecEnvReserved(t *testing.T) {
	_, _, w := workersFixture(t, nil)
	ctx := context.Background()
	lease, err := w.Claim(ctx, workerTenant, "alice", workerClaimReq())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH"} {
		_, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{
			Argv: []string{"true"}, Env: []string{name + "=tcp://evil.invalid:2375"},
		})
		if !errors.Is(err, httpapi.ErrInvalid) {
			t.Fatalf("exec env %s: %v, want invalid", name, err)
		}
	}
	if _, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{
		Argv: []string{"true"}, Env: []string{"HTTP_PROXY=http://proxy:3128", "HOME=/tmp"},
	}); err != nil {
		t.Fatalf("legitimate workload env refused: %v", err)
	}
}

// TestWorkerCellRealDocker is the end-to-end leg: a real store, a real
// docker backend and the shipped cellinit image run claim → exec → release
// with no fakes between the service and the daemon.
func TestWorkerCellRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %s", strings.TrimSpace(string(out)))
	}
	tools := os.Getenv("HUB_CLI_TOOLS_IMAGE")
	if tools == "" {
		tools = "hermes-hub-cli-tools:test"
	}
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", tools).CombinedOutput(); err != nil {
		t.Skipf("image %s unavailable: %s", tools, strings.TrimSpace(string(out)))
	}
	root := t.TempDir()
	// The worker mounts cellinit from the state root; it must be a Linux
	// binary no matter the host OS — cross-build it like the v0 test did.
	cellinit := filepath.Join(root, "cellinit")
	build := exec.CommandContext(ctx, "go", "build", "-o", cellinit, "github.com/letya999/hermes-hub/cmd/cellinit")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-build cellinit: %v (%s)", err, out)
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	s := storetest.Open(t, storetest.SQLiteURL(t), migrations.FS)
	be := backend.NewDocker(backend.DockerConfig{
		ContainerPrefix: "hermes-hub-wkt-",
		NetworkPrefix:   "hermes-hub-wnt-",
		Labels:          map[string]string{"hermes.test": "chg-0336"},
		Trusted:         true,
		StateRoot:       stateRoot,
	})
	// Labelled leftovers from a failed run are safe to remove; keep the
	// cleanup scoped to this test's prefixes.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "hermes-hub-wkt-test-worker").Run()
	})
	w := &Workers{Store: s, Backend: be, Cells: &Cells{
		ToolsImage: tools, CellInitSource: cellinit, StateRoot: stateRoot,
		SeccompProfile: seccomp, Runtime: "runc",
	}}
	imageID, err := exec.CommandContext(ctx, "docker", "image", "inspect", tools, "--format", "{{.Id}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	req := workerClaimReq()
	req.Image = strings.TrimSpace(string(imageID))
	req.Limits = CellLimits{CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64, TimeoutSeconds: 30, OutputBytes: 65536}
	lease, err := w.Claim(ctx, workerTenant, "alice", req)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	res, err := w.Exec(ctx, workerTenant, lease.Worker.ID, lease.Lease, backend.ExecSpec{
		Argv: []string{"echo", "cell-ok"}, Timeout: 15 * time.Second, MaxOutput: 4096,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "cell-ok") {
		t.Fatalf("exec result=%+v", res)
	}
	if err := w.Release(ctx, workerTenant, lease.Worker.ID, lease.Lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Everything the worker owned — container and its network — is gone.
	ps, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=hermes-hub.key="+lease.Worker.ID).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(ps)) != "" {
		t.Fatalf("worker containers survived release: %s", ps)
	}
}
