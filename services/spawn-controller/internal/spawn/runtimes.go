package spawn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/store"
)

// Runtime is one durable runtime row: the identity of a space workload
// and the claim binding Decision 10 fixes at spawn time — tenant,
// policy digest and image digest of the generation. The warm pool is
// the set of rows in warm; nothing warm ever serves a different key.
type Runtime struct {
	ID           string `json:"id"`
	Space        string `json:"space_id"`
	Agent        string `json:"agent"`
	Generation   int64  `json:"generation"`
	State        string `json:"state"`
	PolicyDigest string `json:"policy_digest"`
	ImageDigest  string `json:"image_digest"`
	BackendID    string `json:"backend_id"`
	Address      string `json:"address,omitempty"`
}

// runtimeTransition applies one RuntimeMachine event inside the caller's
// transaction and, whenever the move lands the row outside the leasable
// states (warm, busy), deletes the runtime's lease rows in the same
// transaction (F5/B3) — a draining or missing runtime can never carry a
// hold into its next generation.
func runtimeTransition(q store.Scoped, id, event string, now time.Time) error {
	if err := fsm.Transition(q, RuntimeMachine, id, event, now); err != nil {
		return err
	}
	state, err := fsm.State(q, RuntimeMachine, id)
	if err != nil {
		return err
	}
	if state == "warm" || state == "busy" {
		return nil
	}
	_, err = q.Exec("DELETE FROM leases WHERE {tenant} AND runtime_id = ?", id)
	return err
}

// Runtimes owns the durable runtime lifecycle: claims bind tenant and
// digests to a generation row, the backend start happens outside any
// transaction, and lease rows move the row between warm and busy.
// Image is the image identity stamped on spec and row alike; the real
// pinned digest lands with image pins (S5/S7). MaxLive caps one tenant's
// live rows (starting, warm, busy, draining) at claim time; 0 is
// unbounded.
type Runtimes struct {
	Store      *store.Store
	Backend    backend.Backend
	Image      string
	OCIRuntime string
	MaxLive    int
	Now        func() time.Time
}

func (r *Runtimes) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// runtimeRowID is content-derived per generation — a retried insert of
// the same generation names the same row, and the unique starting index
// keeps concurrent claims to one row per (space, agent).
func runtimeRowID(t store.Tenant, spaceID, agent string, generation int64) string {
	sum := sha256.Sum256([]byte(t.Org + "\x00" + t.Owner + "\x00" + spaceID + "\x00" + agent + "\x00" + fmt.Sprint(generation)))
	return "rt-" + hex.EncodeToString(sum[:])[:16]
}

func loadRuntime(q store.Scoped, id string) (Runtime, error) {
	var rt Runtime
	err := q.QueryRow("SELECT id, space_id, agent, generation, state, policy_digest, image_digest, backend_id, address FROM runtimes WHERE {tenant} AND id = ?", id).
		Scan(&rt.ID, &rt.Space, &rt.Agent, &rt.Generation, &rt.State, &rt.PolicyDigest, &rt.ImageDigest, &rt.BackendID, &rt.Address)
	return rt, err
}

// claimPlan is one transaction's decision: reuse an exact-key warm row,
// wait on someone else's start, or start a new generation.
type claimPlan struct {
	reuse Runtime
	wait  string
	start Runtime
	spec  backend.Spec
}

// errClaimLost marks the lost insert race: a concurrent claim committed
// this (space, agent)'s starting row between the check and the insert —
// the partial unique index refused the second row. Claim recovers by
// waiting on the winner, never by starting a second workload.
var errClaimLost = errors.New("spawn: lost the start race")

// errStaleDigest marks a started runtime whose policy was revoked while
// its backend Start ran: the row is already draining — the claim re-plans
// for the current digest instead of leasing the revoked generation.
var errStaleDigest = errors.New("spawn: policy digest revoked during start")

// Claim resolves one (tenant, space, agent) to a runtime whose workload
// is up and whose row is warm or busy — reusing a warm row only on the
// exact Decision 10 key. A space without a stored effective digest is
// refused with a localized unavailable: no claim ever starts an
// unmanaged runtime. Concurrent claims for the same key start at most
// one backend workload; the losers wait on the starting row and reuse
// it once ready.
func (r *Runtimes) Claim(ctx context.Context, t store.Tenant, spaceID, agent string) (Runtime, error) {
	for {
		plan, err := r.plan(ctx, t, spaceID, agent)
		if err != nil {
			if !errors.Is(err, errClaimLost) {
				return Runtime{}, err
			}
			// The winner's starting row is visible now that both
			// transactions settled; wait on it and re-plan.
			winner, selErr := r.startingRow(ctx, t, spaceID, agent)
			if selErr != nil {
				return Runtime{}, err
			}
			if err := r.waitStarted(ctx, t, winner); err != nil {
				return Runtime{}, err
			}
			continue
		}
		switch {
		case plan.reuse.ID != "":
			return plan.reuse, nil
		case plan.wait != "":
			if err := r.waitStarted(ctx, t, plan.wait); err != nil {
				return Runtime{}, err
			}
		default:
			rt, err := r.start(ctx, plan)
			if errors.Is(err, errStaleDigest) {
				continue // revoked mid-start — re-plan for the current digest
			}
			return rt, err
		}
	}
}

// startingRow is the claim race's recovery read: the winning concurrent
// claim's row, visible only after the loser's own transaction rolled
// back.
func (r *Runtimes) startingRow(ctx context.Context, t store.Tenant, spaceID, agent string) (string, error) {
	var id string
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(t).QueryRow("SELECT id FROM runtimes WHERE {tenant} AND space_id = ? AND agent = ? AND state = 'starting'", spaceID, agent).Scan(&id)
	})
	return id, err
}

// plan is the claim's one write transaction: it reads the space's stored
// effective digest, reuses an exact-key live row, defers to an in-flight
// start, or creates the next generation's starting row. The backend call
// never happens inside — plan hands the spec to the caller (B2).
func (r *Runtimes) plan(ctx context.Context, t store.Tenant, spaceID, agent string) (claimPlan, error) {
	var out claimPlan
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var digest string
		err := q.QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if digest == "" {
			return httpapi.Errorf(httpapi.ErrUnavailable, "%s", locale.T(locale.Default, "spawn.runtime-unavailable"))
		}
		// Exact-key warm reuse only: same digests, same tenant — the row
		// query is tenant-scoped, so nothing cross-tenant is even visible.
		var rt Runtime
		err = q.QueryRow("SELECT id, space_id, agent, generation, state, policy_digest, image_digest, backend_id, address FROM runtimes WHERE {tenant} AND space_id = ? AND agent = ? AND policy_digest = ? AND image_digest = ? AND state IN ('warm','busy') ORDER BY generation DESC LIMIT 1", spaceID, agent, digest, r.Image).
			Scan(&rt.ID, &rt.Space, &rt.Agent, &rt.Generation, &rt.State, &rt.PolicyDigest, &rt.ImageDigest, &rt.BackendID, &rt.Address)
		switch {
		case err == nil:
			out.reuse = rt
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		// Someone else is starting this (space, agent) — digest does not
		// matter: two concurrent starts for one pair is never allowed.
		var starting string
		err = q.QueryRow("SELECT id FROM runtimes WHERE {tenant} AND space_id = ? AND agent = ? AND state = 'starting'", spaceID, agent).Scan(&starting)
		switch {
		case err == nil:
			out.wait = starting
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		if r.MaxLive > 0 {
			var live int
			if err := q.QueryRow("SELECT count(*) FROM runtimes WHERE {tenant} AND state IN ('starting','warm','busy','draining')").Scan(&live); err != nil {
				return err
			}
			if live >= r.MaxLive {
				return httpapi.Errorf(httpapi.ErrUnavailable, "runtime limit reached")
			}
		}
		var gen int64
		if err := q.QueryRow("SELECT COALESCE(MAX(generation),0)+1 FROM runtimes WHERE {tenant} AND space_id = ? AND agent = ?", spaceID, agent).Scan(&gen); err != nil {
			return err
		}
		now := r.now()
		id := runtimeRowID(t, spaceID, agent, gen)
		// The row lands fully formed: space/agent are part of the insert
		// so the partial unique index refuses a second starting row for
		// the same pair at insert, not later. The deadline mirrors
		// fsm.Create's write for the machine's starting bound.
		deadline := now.Add(RuntimeMachine.Deadlines["starting"].After).Unix()
		if err := q.Insert("runtimes", []string{"id", "space_id", "agent", "generation", "state", "deadline", "policy_digest", "image_digest", "backend_id"}, id, spaceID, agent, gen, "starting", deadline, digest, r.Image, id); err != nil {
			return fmt.Errorf("%w: %v", errClaimLost, err)
		}
		if err := q.Insert("runtimes_history", []string{"id", "seq", "at", "from_state", "event", "to_state"}, id, 1, now.Unix(), "", "create", "starting"); err != nil {
			return err
		}
		out.start = Runtime{ID: id, Space: spaceID, Agent: agent, Generation: gen, State: "starting", PolicyDigest: digest, ImageDigest: r.Image, BackendID: id}
		out.spec = backend.Spec{
			Key: id, Org: t.Org, Owner: t.Owner, Space: spaceID, Agent: agent,
			Image: r.Image, OCIRuntime: r.OCIRuntime,
		}
		return nil
	})
	return out, err
}

// waitStarted polls a competitor's row until it leaves starting; the
// claim re-plans on the row's final state. The caller's ctx bounds the
// wait — a stuck starting row also has the machine's own start budget.
func (r *Runtimes) waitStarted(ctx context.Context, t store.Tenant, id string) error {
	for {
		var state string
		err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
			return tx.Scoped(t).QueryRow("SELECT state FROM runtimes WHERE {tenant} AND id = ?", id).Scan(&state)
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
			return httpapi.Errorf(httpapi.ErrUnavailable, "%s", locale.T(locale.Default, "spawn.runtime-unavailable"))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// start runs the backend call outside any transaction, then commits the
// outcome: ready -> warm with the workload's address, or fail — unless an
// activation revoked the row's digest while Start ran, in which case the
// row warms straight into draining in the same transaction (a runtime
// under a revoked policy is never observable warm) and the workload is
// stopped on the spot. A row the sweeper already moved on can no longer
// take ready — the just-started workload is stopped instead of orphaned.
func (r *Runtimes) start(ctx context.Context, plan claimPlan) (Runtime, error) {
	rt := plan.start
	launched, err := r.Backend.Start(ctx, plan.spec)
	if err != nil {
		_ = r.event(ctx, store.Tenant{Org: plan.spec.Org, Owner: plan.spec.Owner}, rt.ID, "fail")
		return Runtime{}, fmt.Errorf("%w: start %s: %v", httpapi.ErrUnavailable, rt.ID, err)
	}
	rt.Address = launched.Address
	t := store.Tenant{Org: plan.spec.Org, Owner: plan.spec.Owner}
	stale := false
	err = r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		if _, err := q.Exec("UPDATE runtimes SET address = ? WHERE {tenant} AND id = ?", rt.Address, rt.ID); err != nil {
			return err
		}
		if err := runtimeTransition(q, rt.ID, "ready", r.now()); err != nil {
			return err
		}
		// Activation only drains warm/busy rows — a runtime whose Start
		// outlives an activation is the gap this check closes. The
		// space's stored pointer is the only truth: anything else on the
		// row (or a gone space row) means revoke.
		var current string
		derr := q.QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", rt.Space).Scan(&current)
		switch {
		case errors.Is(derr, store.ErrNotFound):
		case derr != nil:
			return derr
		}
		if current != rt.PolicyDigest {
			stale = true
			return runtimeTransition(q, rt.ID, "drain", r.now())
		}
		return nil
	})
	if err != nil {
		// The deadline or a drain moved the row while Start ran; the
		// workload it describes must not outlive the row.
		_ = r.Backend.Stop(context.Background(), rt.BackendID)
		return Runtime{}, fmt.Errorf("%w: start %s: %v", httpapi.ErrUnavailable, rt.ID, err)
	}
	if stale {
		// The workload carries a revoked policy — stop it now rather
		// than wait for the sweep to confirm the drain.
		_ = r.Backend.Stop(context.Background(), rt.BackendID)
		return Runtime{}, errStaleDigest
	}
	rt.State = "warm"
	return rt, nil
}

// event fires one runtime transition in its own tenant-scoped
// transaction; a row that already moved on is fine — the event is a
// courtesy, not an assertion.
func (r *Runtimes) event(ctx context.Context, t store.Tenant, id, event string) error {
	return r.Store.Tx(ctx, func(tx *store.Tx) error {
		return runtimeTransition(tx.Scoped(t), id, event, r.now())
	})
}

// Acquire takes a lease on the runtime: warm moves to busy, busy takes
// another hold, anything else refuses — all in one transaction with the
// lease row.
func (r *Runtimes) Acquire(ctx context.Context, t store.Tenant, runtimeID, leaseID string) error {
	return r.Store.Tx(ctx, func(tx *store.Tx) error {
		return AcquireLease(tx.Scoped(t), runtimeID, leaseID, r.now())
	})
}

// Release drops the lease; the last hold moves a still-busy runtime back
// to warm in the same transaction. A lease the drain already removed is
// a no-op — releasing is always safe.
func (r *Runtimes) Release(ctx context.Context, t store.Tenant, runtimeID, leaseID string) error {
	return r.Store.Tx(ctx, func(tx *store.Tx) error {
		err := ReleaseLease(tx.Scoped(t), runtimeID, leaseID, r.now())
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	})
}

// ReleaseLease drops a hold by lease id alone — the contract's DELETE
// route carries only the lease, so the runtime it hangs on is resolved
// in the same transaction. A lease the drain already removed is a no-op:
// releasing is always safe.
func (r *Runtimes) ReleaseLease(ctx context.Context, t store.Tenant, leaseID string) error {
	return r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var runtimeID string
		err := q.QueryRow("SELECT runtime_id FROM leases WHERE {tenant} AND id = ?", leaseID).Scan(&runtimeID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ReleaseLease(q, runtimeID, leaseID, r.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	})
}

// Renew re-arms a held runtime's busy bound (the busy -> busy renew
// edge): the lease row must still exist and the runtime must still be
// busy. Returns the runtime for the caller's bookkeeping.
func (r *Runtimes) Renew(ctx context.Context, t store.Tenant, leaseID string) (Runtime, error) {
	var out Runtime
	err := r.Store.Tx(ctx, func(tx *store.Tx) error {
		q := tx.Scoped(t)
		var runtimeID string
		if err := q.QueryRow("SELECT runtime_id FROM leases WHERE {tenant} AND id = ?", leaseID).Scan(&runtimeID); err != nil {
			return err
		}
		if err := fsm.Transition(q, RuntimeMachine, runtimeID, "renew", r.now()); err != nil {
			return err
		}
		got, err := loadRuntime(q, runtimeID)
		out = got
		return err
	})
	return out, err
}

// List returns the tenant's runtime rows, newest generation first per
// (space, agent) — tombstones included, they are the honest inventory.
func (r *Runtimes) List(ctx context.Context, t store.Tenant) ([]Runtime, error) {
	out := []Runtime{}
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		rows, err := tx.Scoped(t).Query("SELECT id, space_id, agent, generation, state, policy_digest, image_digest, backend_id, address FROM runtimes WHERE {tenant} ORDER BY space_id, agent, generation DESC, id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rt Runtime
			if err := rows.Scan(&rt.ID, &rt.Space, &rt.Agent, &rt.Generation, &rt.State, &rt.PolicyDigest, &rt.ImageDigest, &rt.BackendID, &rt.Address); err != nil {
				return err
			}
			out = append(out, rt)
		}
		return rows.Err()
	})
	return out, err
}
