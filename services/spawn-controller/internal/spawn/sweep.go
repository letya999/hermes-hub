package spawn

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
)

// Sweeper is the controller's one maintenance pass (F2): deadline events
// commit state first and every backend effect happens after, outside any
// transaction — a Stop that hangs can never hold a write lock. The pass
// fails closed on backend failure: a List error marks nothing missing.
//
// Runtimes sweep through runtimeSweep and workers through workerSweep,
// not fsm.Sweep: deadline events must run through the lease-aware
// transition so a drain leaves no lease rows behind in the same
// transaction. Both tables feed effects and reconcile — a backend
// workload whose row is not enumerated is an orphan the reconciler
// stops, so workers are never skipped.
type Sweeper struct {
	Store   *store.Store
	Backend backend.Backend
	// WorkerBackend is the backend worker rows reconcile against; nil
	// means workers share Backend (one-backend installations, tests).
	WorkerBackend backend.Backend
	Now           func() time.Time
}

// backendFor returns the backend a row's workload lives on — worker rows
// on the docker cell backend, runtime rows on the launch backend.
func (s *Sweeper) backendFor(worker bool) backend.Backend {
	if worker && s.WorkerBackend != nil {
		return s.WorkerBackend
	}
	return s.Backend
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Tick runs one pass: deadline sweeps, then the backend effects the new
// states require, then reconciliation against the backend's inventory.
// Controller startup calls it once before serving — boot recovery is the
// same code path as the periodic pass.
func (s *Sweeper) Tick(ctx context.Context) error {
	now := s.now()
	if err := runtimeSweep(ctx, s.Store, now); err != nil {
		return err
	}
	if err := workerSweep(ctx, s.Store, now); err != nil {
		return err
	}
	if err := s.drainStale(ctx, now); err != nil {
		return err
	}
	if err := s.effects(ctx); err != nil {
		return err
	}
	return s.reconcile(ctx, now)
}

// Run loops Tick at interval until ctx ends — the controller wires it
// with HUB_SWEEP_INTERVAL (default 5s).
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Tick(ctx)
		}
	}
}

// runtimeSweep is fsm.Sweep for the runtime machine, firing each expired
// row's deadline event through runtimeTransition so the move out of a
// leasable state removes its holds in the same transaction.
func runtimeSweep(ctx context.Context, s *store.Store, now time.Time) error {
	var expired []store.DueRow
	if err := s.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		expired, err = tx.DueRows(RuntimeMachine.Table, now.Unix(), 1000)
		return err
	}); err != nil {
		return err
	}
	for _, d := range expired {
		deadline, ok := RuntimeMachine.Deadlines[d.State]
		if !ok {
			continue
		}
		err := s.Tx(ctx, func(tx *store.Tx) error {
			q := tx.Scoped(store.Tenant{Org: d.Org, Owner: d.Owner})
			var state string
			var due *int64
			err := q.QueryRow("SELECT state, deadline FROM runtimes WHERE {tenant} AND id = ?", d.ID).Scan(&state, &due)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return fsm.ErrStale
			case err != nil:
				return err
			case state != d.State, due == nil, *due > now.Unix():
				return fsm.ErrStale
			}
			return runtimeTransition(q, d.ID, deadline.Event, now)
		})
		if errors.Is(err, fsm.ErrStale) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// workerSweep is runtimeSweep for the worker machine: deadline events
// fire through workerTransition so the move out of busy removes its
// holds in the same transaction.
func workerSweep(ctx context.Context, s *store.Store, now time.Time) error {
	var expired []store.DueRow
	if err := s.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		expired, err = tx.DueRows(WorkerMachine.Table, now.Unix(), 1000)
		return err
	}); err != nil {
		return err
	}
	for _, d := range expired {
		deadline, ok := WorkerMachine.Deadlines[d.State]
		if !ok {
			continue
		}
		err := s.Tx(ctx, func(tx *store.Tx) error {
			q := tx.Scoped(store.Tenant{Org: d.Org, Owner: d.Owner})
			var state string
			var due *int64
			err := q.QueryRow("SELECT state, deadline FROM workers WHERE {tenant} AND id = ?", d.ID).Scan(&state, &due)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return fsm.ErrStale
			case err != nil:
				return err
			case state != d.State, due == nil, *due > now.Unix():
				return fsm.ErrStale
			}
			return workerTransition(q, d.ID, deadline.Event, now)
		})
		if errors.Is(err, fsm.ErrStale) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// liveRow is a non-final row seen outside any tenant scope — the
// sweeper is the one actor allowed to read across tenants. kind picks
// the machine whose lease-aware transition events go through.
type liveRow struct {
	tenant    store.Tenant
	id        string
	state     string
	backendID string
	worker    bool
}

// transition fires the row's machine event through the lease-aware
// transition for its table.
func (r liveRow) transition(q store.Scoped, event string, now time.Time) error {
	if r.worker {
		return workerTransition(q, r.id, event, now)
	}
	return runtimeTransition(q, r.id, event, now)
}

// liveRows enumerates the non-final rows of one lifecycle table: each
// carries a deadline, so one DueRows batch at the far bound lists the
// live set. The batch is capped by the store (1000 rows per sweep — a
// pass always finishes, a bigger fleet is handled across consecutive
// ticks).
func liveRows(ctx context.Context, s *store.Store, machine *fsm.Machine, worker bool) ([]liveRow, error) {
	var out []liveRow
	err := s.ReadTx(ctx, func(tx *store.Tx) error {
		rows, err := tx.DueRows(machine.Table, math.MaxInt64, 1000)
		if err != nil {
			return err
		}
		for _, d := range rows {
			var backendID string
			err := tx.Scoped(store.Tenant{Org: d.Org, Owner: d.Owner}).
				QueryRow("SELECT backend_id FROM "+machine.Table+" WHERE {tenant} AND id = ?", d.ID).Scan(&backendID)
			if err != nil {
				return err
			}
			out = append(out, liveRow{
				tenant:    store.Tenant{Org: d.Org, Owner: d.Owner},
				id:        d.ID,
				state:     d.State,
				backendID: backendID,
				worker:    worker,
			})
		}
		return nil
	})
	return out, err
}

// drainStale is the revoke backstop: the activation transaction drains
// the warm/busy rows it can see, and this pass drains any row a crash or
// an older version left behind — a warm or busy runtime whose policy
// digest no longer matches its space's stored effective digest, or whose
// space can serve nothing at all. The check re-reads the row inside the
// transaction so a claim that moved it between the listing and here is
// left alone.
func (s *Sweeper) drainStale(ctx context.Context, now time.Time) error {
	rows, err := liveRows(ctx, s.Store, RuntimeMachine, false)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.state != "warm" && row.state != "busy" {
			continue
		}
		err := s.Store.Tx(ctx, func(tx *store.Tx) error {
			q := tx.Scoped(row.tenant)
			var spaceID, digest string
			err := q.QueryRow("SELECT space_id, policy_digest FROM runtimes WHERE {tenant} AND id = ? AND state IN ('warm','busy')", row.id).Scan(&spaceID, &digest)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return fsm.ErrStale
			case err != nil:
				return err
			}
			var current string
			err = q.QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&current)
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return err
			}
			if current != "" && current == digest {
				return nil
			}
			return runtimeTransition(q, row.id, "drain", now)
		})
		if err != nil && !errors.Is(err, fsm.ErrStale) && !errors.Is(err, fsm.ErrForbiddenTransition) {
			return err
		}
	}
	return nil
}

// effects runs the backend actions the committed states call for, in row
// order: a draining row gets its Stop and, when the backend confirms it
// is gone, the stopped event to absent — an unconfirmed stop stays for
// the quarantine deadline, never falsely reported stopped. A failed
// row's half-started workload is stopped so the start budget's teardown
// is real, not just a state label.
func (s *Sweeper) effects(ctx context.Context) error {
	runtimes, err := liveRows(ctx, s.Store, RuntimeMachine, false)
	if err != nil {
		return err
	}
	workers, err := liveRows(ctx, s.Store, WorkerMachine, true)
	if err != nil {
		return err
	}
	rows := append(runtimes, workers...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.backendID == "" {
			continue
		}
		be := s.backendFor(row.worker)
		switch row.state {
		case "draining":
			if err := s.stopConfirmed(ctx, row); err != nil {
				return err
			}
		case "failed":
			// The start budget fired or Start reported an error while a
			// workload survived: stop the half-started backend so failed
			// never leaks a live workload.
			_, err := be.Inspect(ctx, row.backendID)
			switch {
			case errors.Is(err, backend.ErrNotFound):
			case err != nil:
				return err
			default:
				_ = be.Stop(ctx, row.backendID)
			}
		}
	}
	return nil
}

// stopConfirmed stops a draining row's workload and fires stopped when
// the backend confirms it is gone; the row keeps its quarantine bound
// otherwise.
func (s *Sweeper) stopConfirmed(ctx context.Context, row liveRow) error {
	be := s.backendFor(row.worker)
	if err := be.Stop(ctx, row.backendID); err != nil && !errors.Is(err, backend.ErrNotFound) {
		return nil // unconfirmed — leave the row for its quarantine deadline
	}
	_, err := be.Inspect(ctx, row.backendID)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		return s.Store.Tx(ctx, func(tx *store.Tx) error {
			return row.transition(tx.Scoped(row.tenant), "stopped", s.now())
		})
	case err != nil:
		return err
	}
	return nil
}

// reconcile compares rows with the backend's own inventory — List is
// authoritative only on success, so a daemon error changes nothing. A
// serving-state row whose workload is absent moves to missing; a labeled
// workload with no row is an orphan from a crashed controller and is
// stopped. starting rows are left to their own start budget — an
// in-flight Start is not yet in the inventory and must not be declared
// missing under it.
func (s *Sweeper) reconcile(ctx context.Context, now time.Time) error {
	inventory := func(be backend.Backend) (map[string]bool, []backend.Runtime, error) {
		list, err := be.List(ctx)
		if err != nil {
			return nil, nil, err
		}
		live := map[string]bool{}
		for _, rt := range list {
			live[rt.ID] = true
		}
		return live, list, nil
	}
	// Each table reconciles against its own backend: a failed List marks
	// nothing missing (the daemon's answer is authoritative only on
	// success), and each backend's orphans stop through their own backend.
	type scope struct {
		live map[string]bool
		list []backend.Runtime
		be   backend.Backend
		rows []liveRow
	}
	scopes := map[bool]*scope{false: {be: s.Backend}, true: {be: s.backendFor(true)}}
	for worker, sc := range scopes {
		live, list, err := inventory(sc.be)
		if err != nil {
			delete(scopes, worker)
			continue
		}
		sc.live, sc.list = live, list
	}
	if len(scopes) == 0 {
		return nil
	}
	if scopes[false] != nil {
		rows, err := liveRows(ctx, s.Store, RuntimeMachine, false)
		if err != nil {
			return err
		}
		scopes[false].rows = rows
	}
	if scopes[true] != nil {
		rows, err := liveRows(ctx, s.Store, WorkerMachine, true)
		if err != nil {
			return err
		}
		scopes[true].rows = rows
	}
	for _, sc := range scopes {
		known := map[string]bool{}
		for _, row := range sc.rows {
			if row.backendID != "" {
				known[row.backendID] = true
			}
			switch {
			case row.state == "warm" || row.state == "busy":
				if row.backendID == "" || sc.live[row.backendID] {
					continue
				}
				err := s.Store.Tx(ctx, func(tx *store.Tx) error {
					return row.transition(tx.Scoped(row.tenant), "missing", now)
				})
				if err != nil && !errors.Is(err, fsm.ErrForbiddenTransition) && !errors.Is(err, fsm.ErrStale) {
					return err
				}
			case row.state == "draining":
				if row.backendID != "" && !sc.live[row.backendID] {
					// The inventory itself confirms the stop.
					err := s.Store.Tx(ctx, func(tx *store.Tx) error {
						return row.transition(tx.Scoped(row.tenant), "stopped", now)
					})
					if err != nil && !errors.Is(err, fsm.ErrForbiddenTransition) && !errors.Is(err, fsm.ErrStale) {
						return err
					}
				}
			}
		}
		for _, rt := range sc.list {
			if rt.ID != "" && !known[rt.ID] {
				_ = sc.be.Stop(ctx, rt.ID)
			}
		}
	}
	return nil
}
