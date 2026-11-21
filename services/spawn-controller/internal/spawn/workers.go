package spawn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/store"
)

// Worker is one durable worker row: the identity of a disposable cell and
// the claim binding S6 fixes at claim time — tenant, space, cell key and
// image digest of the generation. Workers have no warm state: a released
// worker drains.
type Worker struct {
	ID          string `json:"id"`
	Space       string `json:"space_id"`
	Agent       string `json:"agent"`
	Generation  int64  `json:"generation"`
	State       string `json:"state"`
	CellKey     string `json:"cell_key"`
	ImageDigest string `json:"image_digest"`
	BackendID   string `json:"backend_id"`
	Address     string `json:"address,omitempty"`
	BindingID   string `json:"binding_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`

	canary string      // reuse-guard nonce; never leaves the service
	claim  WorkerClaim // persisted claim, credential values stripped
}

// workerLeaseTTL bounds one hold: a caller that stops renewing loses its
// lease when the sweep next runs, so a crashed consumer can't pin a worker
// forever. Runtime leases keep expires_at = 0 (never expire).
const workerLeaseTTL = 30 * time.Minute

// workerAbsentRetention is how long an absent worker row (and its history)
// is kept for audit before the sweep deletes it. The row's deadline column
// doubles as the GC clock: absent has no machine deadline, so the
// transition writes now+retention there instead of NULL.
const workerAbsentRetention = 24 * time.Hour

// workerTransition applies one WorkerMachine event inside the caller's
// transaction and, whenever the move lands the row outside busy, deletes
// the worker's lease rows in the same transaction — a draining or missing
// worker can never carry a hold into its next generation. A row landing
// absent gets its deadline re-armed as the retention GC clock.
func workerTransition(q store.Scoped, id, event string, now time.Time) error {
	if err := fsm.Transition(q, WorkerMachine, id, event, now); err != nil {
		return err
	}
	state, err := fsm.State(q, WorkerMachine, id)
	if err != nil {
		return err
	}
	if state == "busy" {
		return nil
	}
	if _, err := q.Exec("DELETE FROM leases WHERE {tenant} AND runtime_id = ?", id); err != nil {
		return err
	}
	if state == "absent" {
		_, err = q.Exec("UPDATE workers SET deadline = ? WHERE {tenant} AND id = ?", now.Add(workerAbsentRetention).Unix(), id)
	}
	return err
}

// WorkerLease is a claim's result: the worker plus the hold the caller
// renews and releases, plus the sidecar endpoints the exec environment
// needs ("" when the cell has none).
type WorkerLease struct {
	Worker   Worker
	Lease    string
	Proxy    string // http:// endpoint of the squid sidecar
	CredHost string // sidecar host alias for brokered-credential URLs
}

// Workers owns the durable worker lifecycle: claims bind tenant, space,
// cell key and image digest to a generation row, the backend start happens
// outside any transaction, and lease rows hold the row busy. MaxLive caps
// one tenant's live rows at claim time; 0 is unbounded.
type Workers struct {
	Store   *store.Store
	Backend backend.Backend
	Cells   *Cells
	MaxLive int
	Now     func() time.Time
}

func (w *Workers) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// workerRowID is content-derived per generation — a retried insert of the
// same generation names the same row, and the unique starting index keeps
// concurrent claims to one row per (space, cell key, image).
func workerRowID(t store.Tenant, spaceID, agent, cellKey, image string, generation int64) string {
	sum := sha256.Sum256([]byte(t.Org + "\x00" + t.Owner + "\x00" + spaceID + "\x00" + agent + "\x00" + cellKey + "\x00" + image + "\x00" + fmt.Sprint(generation)))
	return "wk-" + hex.EncodeToString(sum[:])[:16]
}

const workerColumns = "id, space_id, agent, generation, state, cell_key, image_digest, backend_id, address, canary, claim, binding_id, job_id"

func scanWorker(row interface{ Scan(...any) error }) (Worker, error) {
	var w Worker
	var rawClaim string
	err := row.Scan(&w.ID, &w.Space, &w.Agent, &w.Generation, &w.State, &w.CellKey, &w.ImageDigest, &w.BackendID, &w.Address, &w.canary, &rawClaim, &w.BindingID, &w.JobID)
	if err == nil && rawClaim != "" {
		_ = json.Unmarshal([]byte(rawClaim), &w.claim)
	}
	return w, err
}

func loadWorker(q store.Scoped, id string) (Worker, error) {
	return scanWorker(q.QueryRow("SELECT "+workerColumns+" FROM workers WHERE {tenant} AND id = ?", id))
}

// storedClaim renders the claim persisted on the row: everything the
// reuse guard needs to rebuild the worker's spec, with credential values
// stripped — values travel the exec file channel only, never the store.
func storedClaim(req WorkerClaim) (string, error) {
	clean := req
	clean.Brokered = append([]CellCred(nil), req.Brokered...)
	for i := range clean.Brokered {
		clean.Brokered[i].Value = ""
	}
	body, err := json.Marshal(clean)
	return string(body), err
}

func workerLeaseID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "wl-" + hex.EncodeToString(raw)
}

// workerPlan is one transaction's decision: reuse an exact-key busy row,
// wait on someone else's start, or start a new generation.
type workerPlan struct {
	reuse Worker
	wait  string
	start Worker
}

// validateClaim refuses a malformed claim before any transaction: the key
// and image are required, identity fields bounded, and the spec builder
// gets the last word on workspace shape.
func (w *Workers) validateClaim(req WorkerClaim) error {
	ok := func(v string, n int) bool { return v == "" || (len(v) <= n && !hasCtl(v)) }
	if req.CellKey == "" || len(req.CellKey) > 256 || hasCtl(req.CellKey) {
		return httpapi.Errorf(httpapi.ErrInvalid, "worker cell key")
	}
	if req.Image == "" || len(req.Image) > 256 || hasCtl(req.Image) {
		return httpapi.Errorf(httpapi.ErrInvalid, "worker image")
	}
	if !ok(req.Agent, 64) || !ok(req.Principal, 256) || !ok(req.BindingID, 128) || !ok(req.JobID, 128) ||
		!ok(req.Workspace.Path, 1024) || !ok(req.Workspace.Access, 16) {
		return httpapi.Errorf(httpapi.ErrInvalid, "worker identity")
	}
	if len(req.Egress) > 32 || len(req.Brokered) > 8 || len(req.Toolset) > 16 {
		return httpapi.Errorf(httpapi.ErrInvalid, "worker claim shape")
	}
	for _, cred := range req.Brokered {
		if cred.EnvName == "" || cred.Host == "" || cred.Value == "" || len(cred.Value) > 16384 || hasCtl(cred.Value) || len(cred.Prefix) > 128 {
			return httpapi.Errorf(httpapi.ErrInvalid, "worker brokered credential")
		}
	}
	return nil
}

func hasCtl(v string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// errWorkerClaimLost marks the lost insert race: a concurrent claim
// committed this key's starting row between the check and the insert.
var errWorkerClaimLost = errors.New("spawn: lost the worker start race")

// errWorkerDirty marks a reused worker that failed the canary guard: the
// row drains and the claim re-plans for a fresh generation.
var errWorkerDirty = errors.New("spawn: reused worker failed the canary check")

// Claim resolves one (tenant, space, cell key, image) to a ready busy
// worker holding a lease — reusing a busy row only on the exact key, and
// only after the canary guard verifies the cell is still clean. Cells are
// unavailable when the operator did not configure them (V5).
func (w *Workers) Claim(ctx context.Context, t store.Tenant, spaceID string, req WorkerClaim) (WorkerLease, error) {
	if !w.Cells.Enabled() {
		return WorkerLease{}, httpapi.Errorf(httpapi.ErrUnavailable, "%s", locale.T(locale.Default, "spawn.cells-unavailable"))
	}
	if err := w.validateClaim(req); err != nil {
		return WorkerLease{}, err
	}
	for {
		plan, err := w.plan(ctx, t, spaceID, req)
		if err != nil {
			if !errors.Is(err, errWorkerClaimLost) {
				return WorkerLease{}, err
			}
			winner, selErr := w.startingRow(ctx, t, spaceID, req.CellKey, req.Image)
			if selErr != nil {
				return WorkerLease{}, err
			}
			if err := w.waitStarted(ctx, t, winner); err != nil {
				return WorkerLease{}, err
			}
			continue
		}
		switch {
		case plan.reuse.ID != "":
			lease, err := w.reuse(ctx, t, plan.reuse)
			if errors.Is(err, errWorkerDirty) {
				continue // drained — re-plan for a fresh generation
			}
			return lease, err
		case plan.wait != "":
			if err := w.waitStarted(ctx, t, plan.wait); err != nil {
				return WorkerLease{}, err
			}
		default:
			return w.start(ctx, t, spaceID, req, plan.start)
		}
	}
}

// startingRow is the claim race's recovery read, mirroring the runtime
// claim's recovery on the worker key.
func (w *Workers) startingRow(ctx context.Context, t store.Tenant, spaceID, cellKey, image string) (string, error) {
	var id string
	err := w.Store.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(t).QueryRow("SELECT id FROM workers WHERE {tenant} AND space_id = ? AND cell_key = ? AND image_digest = ? AND state = 'starting'", spaceID, cellKey, image).Scan(&id)
	})
	return id, err
}

// plan is the claim's one write transaction: it reuses an exact-key busy
// row, defers to an in-flight start, or creates the next generation's
// starting row. The backend call never happens inside (B2).
func (w *Workers) plan(ctx context.Context, t store.Tenant, spaceID string, req WorkerClaim) (workerPlan, error) {
	var out workerPlan
	err := w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		// Exact-key busy reuse only: same cell key and image, same tenant
		// and space — the row query is tenant-scoped, so nothing
		// cross-tenant is even visible.
		wk, err := scanWorker(q.QueryRow("SELECT "+workerColumns+" FROM workers WHERE {tenant} AND space_id = ? AND cell_key = ? AND image_digest = ? AND state = 'busy' ORDER BY generation DESC LIMIT 1", spaceID, req.CellKey, req.Image))
		switch {
		case err == nil:
			out.reuse = wk
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		var starting string
		err = q.QueryRow("SELECT id FROM workers WHERE {tenant} AND space_id = ? AND cell_key = ? AND image_digest = ? AND state = 'starting'", spaceID, req.CellKey, req.Image).Scan(&starting)
		switch {
		case err == nil:
			out.wait = starting
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		if w.MaxLive > 0 {
			var live int
			if err := q.QueryRow("SELECT count(*) FROM workers WHERE {tenant} AND state IN ('starting','busy','draining')").Scan(&live); err != nil {
				return err
			}
			if live >= w.MaxLive {
				return httpapi.Errorf(httpapi.ErrUnavailable, "worker limit reached")
			}
		}
		var gen int64
		if err := q.QueryRow("SELECT COALESCE(MAX(generation),0)+1 FROM workers WHERE {tenant} AND space_id = ? AND cell_key = ? AND image_digest = ?", spaceID, req.CellKey, req.Image).Scan(&gen); err != nil {
			return err
		}
		rawClaim, err := storedClaim(req)
		if err != nil {
			return err
		}
		now := w.now()
		id := workerRowID(t, spaceID, req.Agent, req.CellKey, req.Image, gen)
		canary := workerLeaseID()
		deadline := now.Add(WorkerMachine.Deadlines["starting"].After).Unix()
		if err := q.Insert("workers", []string{"id", "space_id", "agent", "generation", "state", "deadline", "cell_key", "image_digest", "backend_id", "canary", "claim", "binding_id", "job_id"}, id, spaceID, req.Agent, gen, "starting", deadline, req.CellKey, req.Image, id, canary, rawClaim, req.BindingID, req.JobID); err != nil {
			return fmt.Errorf("%w: %v", errWorkerClaimLost, err)
		}
		if err := q.Insert("workers_history", []string{"id", "seq", "at", "from_state", "event", "to_state"}, id, 1, now.Unix(), "", "create", "starting"); err != nil {
			return err
		}
		out.start = Worker{ID: id, Space: spaceID, Agent: req.Agent, Generation: gen, State: "starting", CellKey: req.CellKey, ImageDigest: req.Image, BackendID: id, BindingID: req.BindingID, JobID: req.JobID, canary: canary, claim: req}
		return nil
	})
	return out, err
}

// waitStarted polls a competitor's row until it leaves starting, mirroring
// the runtime wait.
func (w *Workers) waitStarted(ctx context.Context, t store.Tenant, id string) error {
	for {
		var state string
		err := w.Store.ReadTx(ctx, func(tx *store.Tx) error {
			return tx.Scoped(t).QueryRow("SELECT state FROM workers WHERE {tenant} AND id = ?", id).Scan(&state)
		})
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "starting" {
			return nil
		}
		select {
		case <-ctx.Done():
			return httpapi.Errorf(httpapi.ErrUnavailable, "%s", locale.T(locale.Default, "spawn.cells-unavailable"))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// endpoints renders the sidecar addresses the claim answer carries: the
// squid proxy URL and the cred sidecar's host alias, derived from the
// backend's container name.
func endpoints(name string, req WorkerClaim) (proxy, cred string) {
	if name == "" {
		return "", ""
	}
	if len(realEgress(req.Egress)) > 0 {
		proxy = "http://" + name + "-proxy:" + fmt.Sprint(cellProxyPort)
	}
	if len(req.Brokered) > 0 {
		cred = name + "-cred"
	}
	return proxy, cred
}

// start builds the cell spec, runs the backend call outside any
// transaction, then commits the outcome: the row goes straight to busy
// (workers are leased at readiness — there is no warm), the canary is
// seeded through exec, and the claim's lease row lands in the same final
// transaction.
func (w *Workers) start(ctx context.Context, t store.Tenant, spaceID string, req WorkerClaim, row Worker) (WorkerLease, error) {
	spec, err := w.Cells.spec(t, spaceID, req, row.ID)
	if err != nil {
		_ = w.event(ctx, t, row.ID, "fail")
		return WorkerLease{}, err
	}
	launched, err := w.Backend.Start(ctx, spec)
	if err != nil {
		_ = w.event(ctx, t, row.ID, "fail")
		return WorkerLease{}, fmt.Errorf("%w: start %s: %v", httpapi.ErrUnavailable, row.ID, err)
	}
	row.Address = launched.Name
	// Seed the reuse canary inside the cell before the row is leased: the
	// token file is what the drift check re-verifies (moved from v0
	// createCell's canary step).
	if _, err := w.Backend.Exec(ctx, row.BackendID, backend.ExecSpec{
		Argv: []string{cellInitGuest, "write", cellCanaryPath, row.canary}, Timeout: 15 * time.Second,
	}); err != nil {
		_ = w.Backend.Stop(context.Background(), row.BackendID)
		_ = w.event(ctx, t, row.ID, "fail")
		return WorkerLease{}, fmt.Errorf("%w: canary seed %s", httpapi.ErrUnavailable, row.ID)
	}
	lease := workerLeaseID()
	err = w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if _, err := q.Exec("UPDATE workers SET address = ? WHERE {tenant} AND id = ?", row.Address, row.ID); err != nil {
			return err
		}
		if err := workerTransition(q, row.ID, "ready", w.now()); err != nil {
			return err
		}
		return q.Insert("leases", []string{"id", "runtime_id", "created_at", "expires_at"}, lease, row.ID, w.now().Unix(), w.now().Add(workerLeaseTTL).Unix())
	})
	if err != nil {
		_ = w.Backend.Stop(context.Background(), row.BackendID)
		return WorkerLease{}, fmt.Errorf("%w: start %s: %v", httpapi.ErrUnavailable, row.ID, err)
	}
	row.State = "busy"
	row.canary = ""
	lease2 := WorkerLease{Worker: row, Lease: lease}
	lease2.Proxy, lease2.CredHost = endpoints(row.Address, req)
	return lease2, nil
}

// reuse re-verifies a busy worker before another lease — the moved v0
// warmCell drift check. A worker holding live leases is someone else's
// running cell: probing it mid-exec reads a legitimately dirty state, so
// the claim joins as an additional holder without any probe. Only an
// unleased busy row (a stale hold the sweep has not reached yet) gets the
// full drift check: container profile re-inspected against the spec the
// persisted claim rebuilds (VerifyWorker), then the canary probes through
// backend exec — token file intact, /tmp clean, no stray processes.
func (w *Workers) reuse(ctx context.Context, t store.Tenant, wk Worker) (WorkerLease, error) {
	live, err := w.liveLeaseCount(ctx, t, wk.ID)
	if err != nil {
		return WorkerLease{}, err
	}
	if live == 0 && w.probeDirty(ctx, t, wk) {
		return w.drainOrJoin(ctx, t, wk)
	}
	return w.leaseWorker(ctx, t, wk)
}

// liveLeaseCount counts the worker's unexpired holds — the join decision's
// source of truth.
func (w *Workers) liveLeaseCount(ctx context.Context, t store.Tenant, workerID string) (int, error) {
	var live int
	err := w.Store.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(t).QueryRow("SELECT count(*) FROM leases WHERE {tenant} AND runtime_id = ? AND (expires_at = 0 OR expires_at > ?)", workerID, w.now().Unix()).Scan(&live)
	})
	return live, err
}

// probeDirty runs the reuse guard on an unleased busy row: spec drift via
// VerifyWorker plus the canary exec probes. True means the cell cannot be
// trusted for another holder.
func (w *Workers) probeDirty(ctx context.Context, t store.Tenant, wk Worker) bool {
	spec, specErr := w.Cells.spec(t, wk.Space, wk.claim, wk.ID)
	if specErr != nil || w.Backend.VerifyWorker(ctx, wk.BackendID, spec) != nil {
		return true
	}
	for _, probe := range [][]string{
		{cellInitGuest, "clean", "/tmp", cellCanaryName, wk.canary},
		{cellInitGuest, "procs"},
	} {
		res, err := w.Backend.Exec(ctx, wk.BackendID, backend.ExecSpec{Argv: probe, Timeout: 15 * time.Second})
		if err != nil || res.ExitCode != 0 {
			return true
		}
	}
	return false
}

// drainOrJoin resolves a dirty probe under the row lock: a claim that
// landed a lease between the probe reads and this transaction joins as a
// holder instead of draining the worker it now co-owns.
func (w *Workers) drainOrJoin(ctx context.Context, t store.Tenant, wk Worker) (WorkerLease, error) {
	live, err := w.liveLeaseCount(ctx, t, wk.ID)
	if err != nil {
		return WorkerLease{}, err
	}
	if live > 0 {
		return w.leaseWorker(ctx, t, wk)
	}
	if err := w.event(ctx, t, wk.ID, "drain"); err != nil {
		return WorkerLease{}, err
	}
	return WorkerLease{}, errWorkerDirty
}

// leaseWorker mints the caller's hold on a still-busy row — the CAS keeps
// a release that already drained the row from leasing a dead cell.
func (w *Workers) leaseWorker(ctx context.Context, t store.Tenant, wk Worker) (WorkerLease, error) {
	lease := workerLeaseID()
	err := w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		result, err := q.Exec("UPDATE workers SET deadline = deadline WHERE {tenant} AND id = ? AND state = 'busy'", wk.ID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return fsm.ErrStale
		}
		now := w.now()
		return q.Insert("leases", []string{"id", "runtime_id", "created_at", "expires_at"}, lease, wk.ID, now.Unix(), now.Add(workerLeaseTTL).Unix())
	})
	if err != nil {
		return WorkerLease{}, err
	}
	out := WorkerLease{Worker: wk, Lease: lease}
	out.Proxy, out.CredHost = endpoints(wk.Address, wk.claim)
	out.Worker.canary = ""
	return out, nil
}

// event fires one worker transition in its own tenant-scoped transaction.
func (w *Workers) event(ctx context.Context, t store.Tenant, id, event string) error {
	return w.Store.Tx(ctx, func(tx *store.Tx) error {
		return workerTransition(tx.Scoped(t), id, event, w.now())
	})
}

// Renew re-arms a held worker's busy bound and extends the caller's own
// lease expiry: the lease row must still exist and the worker must still
// be busy.
func (w *Workers) Renew(ctx context.Context, t store.Tenant, workerID, leaseID string) (Worker, error) {
	var out Worker
	err := w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var owner string
		if err := q.QueryRow("SELECT runtime_id FROM leases WHERE {tenant} AND id = ?", leaseID).Scan(&owner); err != nil {
			return err
		}
		if owner != workerID {
			return store.ErrNotFound
		}
		now := w.now()
		if _, err := q.Exec("UPDATE leases SET expires_at = ? WHERE {tenant} AND id = ?", now.Add(workerLeaseTTL).Unix(), leaseID); err != nil {
			return err
		}
		if err := fsm.Transition(q, WorkerMachine, workerID, "renew", now); err != nil {
			return err
		}
		got, err := loadWorker(q, workerID)
		out = got
		return err
	})
	out.canary = ""
	return out, err
}

// Release drops the lease; the last hold moves a still-busy worker to
// draining in the same transaction, then the backend stop and the absent
// tombstone land outside it — the release path itself confirms the stop
// instead of waiting for the sweeper.
func (w *Workers) Release(ctx context.Context, t store.Tenant, workerID, leaseID string) error {
	drained := false
	err := w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		result, err := q.Exec("DELETE FROM leases WHERE {tenant} AND id = ? AND runtime_id = ?", leaseID, workerID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return store.ErrNotFound
		}
		var remaining int
		if err := q.QueryRow("SELECT count(*) FROM leases WHERE {tenant} AND runtime_id = ?", workerID).Scan(&remaining); err != nil {
			return err
		}
		if remaining > 0 {
			return nil
		}
		state, err := fsm.State(q, WorkerMachine, workerID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil || state != "busy" {
			return err
		}
		drained = true
		return workerTransition(q, workerID, "unlease", w.now())
	})
	if err != nil || !drained {
		return err
	}
	if err := w.Backend.Stop(ctx, workerID); err != nil && !errors.Is(err, backend.ErrNotFound) {
		return nil // unconfirmed — the draining row's quarantine deadline owns it
	}
	_, err = w.Backend.Inspect(ctx, workerID)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		return w.event(ctx, t, workerID, "stopped")
	case err != nil:
		return err
	}
	return nil
}

// deniedExecEnv rejects env names that must never reach the exec request:
// a value in spec env lands in the docker CLI child's own environment for
// -e forwarding, and DOCKER_* names reconfigure that CLI (host, context,
// config dir, TLS). The workload has no legitimate use for them.
func deniedExecEnv(name string) bool {
	return name == "DOCKER" || strings.HasPrefix(name, "DOCKER_")
}

// Exec runs one bounded command inside a busy worker whose lease the
// caller holds; a worker that left busy refuses.
func (w *Workers) Exec(ctx context.Context, t store.Tenant, workerID, leaseID string, spec backend.ExecSpec) (backend.ExecResult, error) {
	for _, entry := range spec.Env {
		name, _, _ := strings.Cut(entry, "=")
		if deniedExecEnv(name) {
			return backend.ExecResult{}, httpapi.Errorf(httpapi.ErrInvalid, "%s", locale.T(locale.Default, "spawn.exec-env-reserved"))
		}
	}
	err := w.Store.ReadTx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var owner string
		if err := q.QueryRow("SELECT runtime_id FROM leases WHERE {tenant} AND id = ?", leaseID).Scan(&owner); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpapi.Errorf(httpapi.ErrNotFound, "exec: lease not found")
			}
			return err
		}
		if owner != workerID {
			return httpapi.Errorf(httpapi.ErrNotFound, "exec: lease not held by worker")
		}
		var state string
		if err := q.QueryRow("SELECT state FROM workers WHERE {tenant} AND id = ?", workerID).Scan(&state); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpapi.Errorf(httpapi.ErrNotFound, "exec: worker not found")
			}
			return err
		}
		if state != "busy" {
			return fsm.ErrForbiddenTransition
		}
		return nil
	})
	if err != nil {
		return backend.ExecResult{}, err
	}
	return w.Backend.Exec(ctx, workerID, spec)
}

// Drain moves a space's live workers to draining — optionally only those
// claimed under one binding or job — and confirms each backend stop: a
// worker the backend reports gone tombs absent in the same pass, a stuck
// one stays draining for the quarantine deadline (the v0 cleanupCells
// pass, moved).
func (w *Workers) Drain(ctx context.Context, t store.Tenant, spaceID, bindingID, jobID string) (int, error) {
	var rows []Worker
	err := w.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		query := "SELECT " + workerColumns + " FROM workers WHERE {tenant} AND space_id = ? AND state IN ('starting','busy','draining')"
		args := []any{spaceID}
		if bindingID != "" {
			query += " AND binding_id = ?"
			args = append(args, bindingID)
		}
		if jobID != "" {
			query += " AND job_id = ?"
			args = append(args, jobID)
		}
		found, err := q.Query(query, args...)
		if err != nil {
			return err
		}
		defer found.Close()
		for found.Next() {
			wk, err := scanWorker(found)
			if err != nil {
				return err
			}
			rows = append(rows, wk)
		}
		if err := found.Err(); err != nil {
			return err
		}
		// Transitions run inside the selection transaction: a starting
		// worker's drain aborts its start (fail), a busy one drains and
		// drops its leases, an already-draining row needs no event.
		for _, wk := range rows {
			switch wk.State {
			case "starting":
				if err := workerTransition(q, wk.ID, "fail", w.now()); err != nil {
					return err
				}
			case "busy":
				if err := workerTransition(q, wk.ID, "drain", w.now()); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	drained := 0
	for _, wk := range rows {
		if wk.State == "starting" {
			drained++
			continue // backend start never committed; nothing to stop
		}
		if err := w.Backend.Stop(ctx, wk.BackendID); err != nil && !errors.Is(err, backend.ErrNotFound) {
			drained++ // unconfirmed — the draining row's quarantine deadline owns it
			continue
		}
		if _, err := w.Backend.Inspect(ctx, wk.BackendID); errors.Is(err, backend.ErrNotFound) {
			_ = w.event(ctx, t, wk.ID, "stopped")
		}
		drained++
	}
	return drained, nil
}

// workerListLimit bounds one List read — worker rows accumulate per call,
// so an unbounded enumeration is a memory and latency hazard. Consumers
// that need the live set use drain/claim, not List.
const workerListLimit = 500

// List returns the tenant's worker rows, newest generation first per key —
// tombstones included, capped at workerListLimit.
func (w *Workers) List(ctx context.Context, t store.Tenant) ([]Worker, error) {
	out := []Worker{}
	err := w.Store.ReadTx(ctx, func(tx *store.Tx) error {
		rows, err := tx.Scoped(t).Query("SELECT "+workerColumns+" FROM workers WHERE {tenant} ORDER BY space_id, cell_key, generation DESC, id LIMIT ?", workerListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			wk, err := scanWorker(rows)
			if err != nil {
				return err
			}
			wk.canary = ""
			wk.claim = WorkerClaim{}
			out = append(out, wk)
		}
		return rows.Err()
	})
	return out, err
}
