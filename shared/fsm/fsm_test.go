package fsm_test

import (
	"errors"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var schema = fstest.MapFS{"0001_jobs.sql": {Data: []byte(`
CREATE TABLE jobs (org TEXT NOT NULL, owner TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL, deadline BIGINT, PRIMARY KEY (org, owner, id));
CREATE TABLE job_history (org TEXT NOT NULL, owner TEXT NOT NULL, id TEXT NOT NULL, seq INTEGER NOT NULL, at BIGINT NOT NULL, from_state TEXT NOT NULL, event TEXT NOT NULL, to_state TEXT NOT NULL, PRIMARY KEY (org, owner, id, seq));
`)}}

func jobMachine(t *testing.T) *fsm.Machine {
	t.Helper()
	m, err := fsm.New(fsm.Machine{
		Table: "jobs", History: "job_history", Initial: "queued",
		Edges: []fsm.Edge{
			{From: "queued", Event: "admit", To: "running"},
			{From: "queued", Event: "expire", To: "expired"},
			{From: "running", Event: "finish", To: "succeeded"},
			{From: "running", Event: "timeout", To: "uncertain"},
			{From: "uncertain", Event: "observe", To: "succeeded"},
			{From: "uncertain", Event: "escalate", To: "failed"},
		},
		Deadlines: map[string]fsm.Deadline{
			"queued":    {After: time.Hour, Event: "expire"},
			"running":   {After: 5 * time.Minute, Event: "timeout"},
			"uncertain": {After: 24 * time.Hour, Event: "escalate"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var (
	alice = store.Tenant{Org: "org-a", Owner: "alice"}
	bob   = store.Tenant{Org: "org-b", Owner: "bob"}
	t0    = time.Unix(1_800_000_000, 0)
)

func tx(t *testing.T, s *store.Store, fn func(*store.Tx) error) error {
	t.Helper()
	return s.Tx(t.Context(), fn)
}

func create(t *testing.T, s *store.Store, m *fsm.Machine, tenant store.Tenant, id string, now time.Time) {
	t.Helper()
	if err := tx(t, s, func(tx *store.Tx) error { return fsm.Create(tx.Scoped(tenant), m, id, now) }); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

type snapshot struct {
	state    string
	deadline *int64
	history  int
}

func read(t *testing.T, s *store.Store, tenant store.Tenant, id string) snapshot {
	t.Helper()
	var snap snapshot
	err := tx(t, s, func(tx *store.Tx) error {
		q := tx.Scoped(tenant)
		if err := q.QueryRow("SELECT state, deadline FROM jobs WHERE {tenant} AND id = ?", id).Scan(&snap.state, &snap.deadline); err != nil {
			return err
		}
		return q.QueryRow("SELECT count(*) FROM job_history WHERE {tenant} AND id = ?", id).Scan(&snap.history)
	})
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return snap
}

func TestTransitionRejectsForbiddenAndWritesNothing(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, schema)
		m := jobMachine(t)
		create(t, s, m, alice, "j1", t0)
		before := read(t, s, alice, "j1")
		for _, event := range []string{"finish", "observe", "unknown", ""} {
			err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "j1", event, t0) })
			if !errors.Is(err, fsm.ErrForbiddenTransition) || !errors.Is(err, store.ErrConflict) {
				t.Errorf("event %q from queued: want forbidden conflict, got %v", event, err)
			}
		}
		err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(bob), m, "j1", "admit", t0) })
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("foreign tenant: want not_found, got %v", err)
		}
		err = tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "missing", "admit", t0) })
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("missing entity: want not_found, got %v", err)
		}
		after := read(t, s, alice, "j1")
		if after.state != before.state || after.history != before.history || *after.deadline != *before.deadline {
			t.Fatalf("refused transitions wrote: before %+v after %+v", before, after)
		}
	})
}

func TestTransitionWritesStateAndHistory(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, schema)
		m := jobMachine(t)
		create(t, s, m, alice, "j1", t0)
		if snap := read(t, s, alice, "j1"); snap.state != "queued" || snap.history != 1 || *snap.deadline != t0.Add(time.Hour).Unix() {
			t.Fatalf("created: %+v", snap)
		}
		admitted := t0.Add(time.Minute)
		if err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "j1", "admit", admitted) }); err != nil {
			t.Fatal(err)
		}
		if err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "j1", "finish", admitted) }); err != nil {
			t.Fatal(err)
		}
		snap := read(t, s, alice, "j1")
		if snap.state != "succeeded" || snap.deadline != nil || snap.history != 3 {
			t.Fatalf("final state must clear the deadline and keep history: %+v", snap)
		}
		var from, event, to string
		var seq int
		err := tx(t, s, func(tx *store.Tx) error {
			return tx.Scoped(alice).QueryRow("SELECT seq, from_state, event, to_state FROM job_history WHERE {tenant} AND id = ? AND seq = ?", "j1", 2).Scan(&seq, &from, &event, &to)
		})
		if err != nil || from != "queued" || event != "admit" || to != "running" {
			t.Fatalf("history row 2: %d %s %s %s %v", seq, from, event, to, err)
		}
		// A caller failing after the transition rolls state and history back.
		err = tx(t, s, func(tx *store.Tx) error {
			if err := fsm.Create(tx.Scoped(alice), m, "j2", t0); err != nil {
				return err
			}
			if err := fsm.Transition(tx.Scoped(alice), m, "j2", "admit", t0); err != nil {
				return err
			}
			return errors.New("caller fails after the transition")
		})
		if err == nil {
			t.Fatal("tx error lost")
		}
		err = tx(t, s, func(tx *store.Tx) error { _, err := fsm.State(tx.Scoped(alice), m, "j2"); return err })
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rolled-back create visible: %v", err)
		}
	})
}

func TestSweepMovesExpiredState(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, schema)
		m := jobMachine(t)
		for _, tenant := range []store.Tenant{alice, bob} {
			create(t, s, m, tenant, "run", t0)
			if err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(tenant), m, "run", "admit", t0) }); err != nil {
				t.Fatal(err)
			}
			create(t, s, m, tenant, "fresh", t0.Add(10*time.Minute))
			create(t, s, m, tenant, "done", t0)
			if err := tx(t, s, func(tx *store.Tx) error {
				if err := fsm.Transition(tx.Scoped(tenant), m, "done", "admit", t0); err != nil {
					return err
				}
				return fsm.Transition(tx.Scoped(tenant), m, "done", "finish", t0)
			}); err != nil {
				t.Fatal(err)
			}
		}
		sweep := func(now time.Time) int {
			n, err := fsm.Sweep(t.Context(), s, m, now, 100)
			if err != nil {
				t.Fatalf("sweep at %v: %v", now, err)
			}
			return n
		}
		if n := sweep(t0.Add(4 * time.Minute)); n != 0 {
			t.Fatalf("sweep before any deadline moved %d", n)
		}
		if n := sweep(t0.Add(6 * time.Minute)); n != 2 {
			t.Fatalf("sweep after running deadline moved %d, want 2", n)
		}
		for _, tenant := range []store.Tenant{alice, bob} {
			if snap := read(t, s, tenant, "run"); snap.state != "uncertain" || snap.history != 3 || *snap.deadline != t0.Add(6*time.Minute+24*time.Hour).Unix() {
				t.Errorf("%s run after sweep: %+v", tenant.Owner, snap)
			}
			if snap := read(t, s, tenant, "fresh"); snap.state != "queued" || snap.history != 1 {
				t.Errorf("%s fresh after sweep: %+v", tenant.Owner, snap)
			}
			if snap := read(t, s, tenant, "done"); snap.state != "succeeded" || snap.history != 3 {
				t.Errorf("%s done after sweep: %+v", tenant.Owner, snap)
			}
		}
		// The swept uncertain job is never retried: its own deadline leads
		// to failed, not back to running.
		if n := sweep(t0.Add(25 * time.Hour)); n != 4 {
			t.Fatalf("second sweep moved %d, want 4 (2 uncertain, 2 queued)", n)
		}
		if snap := read(t, s, alice, "run"); snap.state != "failed" || snap.deadline != nil {
			t.Fatalf("uncertain after its deadline: %+v", snap)
		}
	})
}

// Sweep moves each due row in its own transaction, so one row another
// actor took is skipped rather than rolling the batch back: two sweeps
// racing one deadline move it exactly once between them.
func TestSweepSkipsStaleAndConcurrentSweepersLoseCleanly(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, schema)
		m := jobMachine(t)
		create(t, s, m, alice, "j1", t0)
		create(t, s, m, alice, "j2", t0)
		create(t, s, m, bob, "j3", t0)
		// A concurrent actor moves j1 between the sweep's read and write: its
		// compare-and-swap must report stale and the sweep must go on.
		if err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "j1", "admit", t0) }); err != nil {
			t.Fatal(err)
		}
		moved, err := fsm.Sweep(t.Context(), s, m, t0.Add(2*time.Hour), 100)
		if err != nil {
			t.Fatalf("sweep across tenants: %v", err)
		}
		if moved != 3 {
			t.Fatalf("sweep moved %d, want 3 (j1 timeout, j2+j3 expired)", moved)
		}
		for id, want := range map[string]string{"j1": "uncertain", "j2": "expired", "j3": "expired"} {
			var state string
			err := tx(t, s, func(tx *store.Tx) error {
				owner := alice
				if id == "j3" {
					owner = bob
				}
				return tx.Scoped(owner).QueryRow("SELECT state FROM jobs WHERE {tenant} AND id = ?", id).Scan(&state)
			})
			if err != nil || state != want {
				t.Fatalf("%s state=%q want %q err %v", id, state, want, err)
			}
		}

		// Two sweeps racing one deadline move the row exactly once between
		// them; the loser reports its rows stale and skips them.
		s2 := storetest.Open(t, url, schema)
		defer s2.Close()
		create(t, s, m, alice, "race", t0)
		var wg sync.WaitGroup
		counts := make([]int, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, st := range []*store.Store{s, s2} {
			wg.Add(1)
			go func(i int, st *store.Store) {
				defer wg.Done()
				<-start
				counts[i], errs[i] = fsm.Sweep(t.Context(), st, m, t0.Add(2*time.Hour), 100)
			}(i, st)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("sweeper %d: %v", i, err)
			}
		}
		var state string
		err = tx(t, s, func(tx *store.Tx) error {
			return tx.Scoped(alice).QueryRow("SELECT state FROM jobs WHERE {tenant} AND id = ?", "race").Scan(&state)
		})
		if err != nil || state != "expired" {
			t.Fatalf("race row state=%q err %v (sweeps moved %v)", state, err, counts)
		}
	})
}

// The window between the sweep's read-only scan and the per-row write is
// where a concurrent actor wins: the row must be re-verified inside the
// write transaction or the stale event fires early (or the whole batch
// dies on a forbidden edge).
func TestSweepRowReverifiesInsideWriteTransaction(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, schema)
		m := jobMachine(t)
		create(t, s, m, alice, "j1", t0)
		create(t, s, m, alice, "j2", t0)
		now := t0.Add(2 * time.Hour)
		// Scan exactly what the sweeper would see.
		var due []store.DueRow
		if err := s.ReadTx(t.Context(), func(tx *store.Tx) error {
			var err error
			due, err = tx.DueRows("jobs", now.Unix(), 100)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(due) != 2 {
			t.Fatalf("scan: %+v", due)
		}
		var j1, j2 store.DueRow
		for _, d := range due {
			switch d.ID {
			case "j1":
				j1 = d
			case "j2":
				j2 = d
			}
		}
		// A concurrent actor moves j1 out of the scanned state: the stale
		// "expire" event is forbidden from "running" — before the fix this
		// returned ErrForbiddenTransition and killed the whole batch.
		if err := tx(t, s, func(tx *store.Tx) error { return fsm.Transition(tx.Scoped(alice), m, "j1", "admit", now) }); err != nil {
			t.Fatal(err)
		}
		if err := fsm.SweepRow(t.Context(), s, m, j1, now); !errors.Is(err, fsm.ErrStale) {
			t.Fatalf("moved row: %v, want ErrStale", err)
		}
		if state, err := currentState(t, s, alice, "j1"); err != nil || state != "running" {
			t.Fatalf("j1 state=%q err %v — expire fired on a moved row", state, err)
		}
		// j2 is untouched and still sweeps through the same path.
		if err := fsm.SweepRow(t.Context(), s, m, j2, now); err != nil {
			t.Fatalf("clean row: %v", err)
		}
		if state, err := currentState(t, s, alice, "j2"); err != nil || state != "expired" {
			t.Fatalf("j2 state=%q err %v", state, err)
		}

		// A row still in the scanned state but re-armed onto a fresh deadline
		// by a concurrent writer must not fire early: state matches, deadline
		// does not — the row is stale for this scan.
		create(t, s, m, alice, "j3", now)
		var j3 store.DueRow
		if err := s.ReadTx(t.Context(), func(tx *store.Tx) error {
			var err error
			rows, err := tx.DueRows("jobs", now.Add(90*time.Minute).Unix(), 100)
			for _, d := range rows {
				if d.ID == "j3" {
					j3 = d
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if j3.ID != "j3" {
			t.Fatal("fixture: j3 not due")
		}
		// A concurrent transition refreshed j3's deadline far past the scan
		// instant while leaving it in "queued".
		later := now.Add(72 * time.Hour)
		if err := tx(t, s, func(tx *store.Tx) error {
			res, err := tx.Scoped(alice).Exec("UPDATE jobs SET deadline = ? WHERE {tenant} AND id = ?", later.Unix(), "j3")
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil || n != 1 {
				t.Fatalf("re-arm rows=%d err=%v", n, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := fsm.SweepRow(t.Context(), s, m, j3, now.Add(90*time.Minute)); !errors.Is(err, fsm.ErrStale) {
			t.Fatalf("re-armed row: %v, want ErrStale", err)
		}
		if state, err := currentState(t, s, alice, "j3"); err != nil || state != "queued" {
			t.Fatalf("j3 state=%q err %v — expire fired early", state, err)
		}
	})
}

func currentState(t *testing.T, s *store.Store, tenant store.Tenant, id string) (string, error) {
	t.Helper()
	var state string
	err := tx(t, s, func(tx *store.Tx) error {
		return tx.Scoped(tenant).QueryRow("SELECT state FROM jobs WHERE {tenant} AND id = ?", id).Scan(&state)
	})
	return state, err
}

func TestMachineRequiresDeadlinePerNonFinalState(t *testing.T) {
	base := func() fsm.Machine {
		return fsm.Machine{
			Table: "jobs", History: "job_history", Initial: "queued",
			Edges:     []fsm.Edge{{From: "queued", Event: "run", To: "done"}, {From: "queued", Event: "expire", To: "expired"}},
			Deadlines: map[string]fsm.Deadline{"queued": {After: time.Hour, Event: "expire"}},
		}
	}
	if _, err := fsm.New(base()); err != nil {
		t.Fatalf("valid machine: %v", err)
	}
	broken := map[string]func(*fsm.Machine){
		"missing deadline":      func(m *fsm.Machine) { m.Deadlines = nil },
		"deadline without edge": func(m *fsm.Machine) { m.Deadlines["queued"] = fsm.Deadline{After: time.Hour, Event: "nope"} },
		"deadline on final":     func(m *fsm.Machine) { m.Deadlines["done"] = fsm.Deadline{After: time.Hour, Event: "run"} },
		"zero deadline":         func(m *fsm.Machine) { m.Deadlines["queued"] = fsm.Deadline{Event: "expire"} },
		"duplicate edge": func(m *fsm.Machine) {
			m.Edges = append(m.Edges, fsm.Edge{From: "queued", Event: "run", To: "expired"})
		},
		"bad table":             func(m *fsm.Machine) { m.Table = "jobs; DROP TABLE x" },
		"initial without edges": func(m *fsm.Machine) { m.Initial = "done" },
		"empty event":           func(m *fsm.Machine) { m.Edges = append(m.Edges, fsm.Edge{From: "done", To: "queued"}) },
	}
	for name, mutate := range broken {
		m := base()
		mutate(&m)
		if _, err := fsm.New(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTenantInvariants(t *testing.T) {
	m := jobMachine(t)
	storetest.TenantInvariants(t, storetest.TenantCase{
		Table: "jobs", Migrations: schema,
		Create: func(q store.Scoped) (string, error) { return "j1", fsm.Create(q, m, "j1", t0) },
		Read:   func(q store.Scoped, id string) error { _, err := fsm.State(q, m, id); return err },
		Write: func(q store.Scoped, id string) (bool, error) {
			return false, fsm.Transition(q, m, id, "admit", t0)
		},
	}, storetest.TenantCase{
		Table: "job_history", Migrations: schema,
		Create: func(q store.Scoped) (string, error) { return "j1", fsm.Create(q, m, "j1", t0) },
		Read: func(q store.Scoped, id string) error {
			var seq int
			return q.QueryRow("SELECT seq FROM job_history WHERE {tenant} AND id = ?", id).Scan(&seq)
		},
		Write: func(q store.Scoped, id string) (bool, error) {
			res, err := q.Exec("UPDATE job_history SET event = ? WHERE {tenant} AND id = ?", "forged", id)
			if err != nil {
				return false, err
			}
			n, err := res.RowsAffected()
			return n > 0, err
		},
	})
}
