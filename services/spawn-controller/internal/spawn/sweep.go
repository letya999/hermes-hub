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
// Runtimes sweep through runtimeSweep, not fsm.Sweep: the runtime
// machine's deadline events must run through runtimeTransition so a
// drain leaves no lease rows behind in the same transaction. Workers
// hold no leases, so the generic sweep serves them.
type Sweeper struct {
	Store   *store.Store
	Backend backend.Backend
	Now     func() time.Time
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
	if _, err := fsm.Sweep(ctx, s.Store, WorkerMachine, now, 1000); err != nil {
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

// liveRuntimeRow is a non-final row seen outside any tenant scope — the
// sweeper is the one actor allowed to read across tenants.
type liveRuntimeRow struct {
	tenant    store.Tenant
	id        string
	state     string
	backendID string
}

// liveRows enumerates the non-final runtime rows: each carries a
// deadline, so one DueRows batch at the far bound lists the live set.
// The batch is capped by the store (1000 rows per sweep — a pass always
// finishes, a bigger fleet is handled across consecutive ticks).
func liveRows(ctx context.Context, s *store.Store) ([]liveRuntimeRow, error) {
	var out []liveRuntimeRow
	err := s.ReadTx(ctx, func(tx *store.Tx) error {
		rows, err := tx.DueRows(RuntimeMachine.Table, math.MaxInt64, 1000)
		if err != nil {
			return err
		}
		for _, d := range rows {
			var backendID string
			err := tx.Scoped(store.Tenant{Org: d.Org, Owner: d.Owner}).
				QueryRow("SELECT backend_id FROM runtimes WHERE {tenant} AND id = ?", d.ID).Scan(&backendID)
			if err != nil {
				return err
			}
			out = append(out, liveRuntimeRow{
				tenant:    store.Tenant{Org: d.Org, Owner: d.Owner},
				id:        d.ID,
				state:     d.State,
				backendID: backendID,
			})
		}
		return nil
	})
	return out, err
}

// effects runs the backend actions the committed states call for, in row
// order: a draining row gets its Stop and, when the backend confirms it
// is gone, the stopped event to absent — an unconfirmed stop stays for
// the quarantine deadline, never falsely reported stopped. A failed
// row's half-started workload is stopped so the start budget's teardown
// is real, not just a state label.
func (s *Sweeper) effects(ctx context.Context) error {
	rows, err := liveRows(ctx, s.Store)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.backendID == "" {
			continue
		}
		switch row.state {
		case "draining":
			if err := s.stopConfirmed(ctx, row); err != nil {
				return err
			}
		case "failed":
			// The start budget fired or Start reported an error while a
			// workload survived: stop the half-started backend so failed
			// never leaks a live workload.
			_, err := s.Backend.Inspect(ctx, row.backendID)
			switch {
			case errors.Is(err, backend.ErrNotFound):
			case err != nil:
				return err
			default:
				_ = s.Backend.Stop(ctx, row.backendID)
			}
		}
	}
	return nil
}

// stopConfirmed stops a draining row's workload and fires stopped when
// the backend confirms it is gone; the row keeps its quarantine bound
// otherwise.
func (s *Sweeper) stopConfirmed(ctx context.Context, row liveRuntimeRow) error {
	if err := s.Backend.Stop(ctx, row.backendID); err != nil && !errors.Is(err, backend.ErrNotFound) {
		return nil // unconfirmed — leave the row for its quarantine deadline
	}
	_, err := s.Backend.Inspect(ctx, row.backendID)
	switch {
	case errors.Is(err, backend.ErrNotFound):
		return s.Store.Tx(ctx, func(tx *store.Tx) error {
			return runtimeTransition(tx.Scoped(row.tenant), row.id, "stopped", s.now())
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
	list, err := s.Backend.List(ctx)
	if err != nil {
		return nil
	}
	live := map[string]bool{}
	for _, rt := range list {
		live[rt.ID] = true
	}
	rows, err := liveRows(ctx, s.Store)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, row := range rows {
		if row.backendID != "" {
			known[row.backendID] = true
		}
		switch {
		case row.state == "warm" || row.state == "busy":
			if row.backendID == "" || live[row.backendID] {
				continue
			}
			err := s.Store.Tx(ctx, func(tx *store.Tx) error {
				return runtimeTransition(tx.Scoped(row.tenant), row.id, "missing", now)
			})
			if err != nil && !errors.Is(err, fsm.ErrForbiddenTransition) && !errors.Is(err, fsm.ErrStale) {
				return err
			}
		case row.state == "draining":
			if row.backendID != "" && !live[row.backendID] {
				// The inventory itself confirms the stop.
				err := s.Store.Tx(ctx, func(tx *store.Tx) error {
					return runtimeTransition(tx.Scoped(row.tenant), row.id, "stopped", now)
				})
				if err != nil && !errors.Is(err, fsm.ErrForbiddenTransition) && !errors.Is(err, fsm.ErrStale) {
					return err
				}
			}
		}
	}
	for _, rt := range list {
		if rt.ID != "" && !known[rt.ID] {
			_ = s.Backend.Stop(ctx, rt.ID)
		}
	}
	return nil
}
