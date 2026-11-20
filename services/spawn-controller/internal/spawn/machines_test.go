package spawn_test

import (
	"errors"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn/migrations"
	"github.com/letya999/hermes-hub/shared/fsm"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
)

var (
	tenant = storetest.Owner
	t0     = time.Unix(1_800_000_000, 0)
)

func create(t *testing.T, s *store.Store, m *fsm.Machine, id string) {
	t.Helper()
	if err := s.Tx(t.Context(), func(tx *store.Tx) error {
		return fsm.Create(tx.Scoped(tenant), m, id, t0)
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func transition(t *testing.T, s *store.Store, m *fsm.Machine, id, event string) error {
	return s.Tx(t.Context(), func(tx *store.Tx) error {
		return fsm.Transition(tx.Scoped(tenant), m, id, event, t0)
	})
}

// read returns the entity's current state and its history row count.
func read(t *testing.T, s *store.Store, m *fsm.Machine, id string) (string, int) {
	t.Helper()
	var state string
	var history int
	err := s.Tx(t.Context(), func(tx *store.Tx) error {
		q := tx.Scoped(tenant)
		if err := q.QueryRow("SELECT state FROM "+m.Table+" WHERE {tenant} AND id = ?", id).Scan(&state); err != nil {
			return err
		}
		return q.QueryRow("SELECT count(*) FROM "+m.History+" WHERE {tenant} AND id = ?", id).Scan(&history)
	})
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return state, history
}

// checkTable drives every allowed edge of a machine: path events move the
// entity from its initial state to the edge's From, then event must land on
// want and append exactly one history row. A sample of forbidden events on
// the reached state writes nothing.
func checkTable(t *testing.T, m *fsm.Machine, cases []struct {
	path  []string
	event string
	want  string
}, forbidden map[string][]string) {
	t.Helper()
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, migrations.FS)
		for i, tc := range cases {
			id := string(rune('a' + i))
			create(t, s, m, id)
			for _, event := range tc.path {
				if err := transition(t, s, m, id, event); err != nil {
					t.Fatalf("path %q on %s: %v", event, id, err)
				}
			}
			_, history := read(t, s, m, id)
			if err := transition(t, s, m, id, tc.event); err != nil {
				t.Fatalf("edge %q on %s: %v", tc.event, id, err)
			}
			state, after := read(t, s, m, id)
			if state != tc.want || after != history+1 {
				t.Fatalf("%s after %q: state %q want %q, history %d want %d", id, tc.event, state, tc.want, after, history+1)
			}
			for _, event := range forbidden[tc.want] {
				if err := transition(t, s, m, id, event); !errors.Is(err, fsm.ErrForbiddenTransition) {
					t.Fatalf("%s: forbidden %q from %q: %v", id, event, state, err)
				}
			}
			if state, final := read(t, s, m, id); state != tc.want || final != after {
				t.Fatalf("%s: forbidden events wrote: state %q history %d", id, state, final)
			}
		}
	})
}

// The revision machine: draft, validated, rejected, active, superseded,
// pruned. Rollback is an activate on a superseded row; expire tombstones
// stale drafts and unactivated validated rows, retain is the sweeper's
// self-loop that keeps active and superseded alive forever, and prune is
// the revision service's explicit retention edge — superseded rows beyond
// the keep slack move to the final pruned state.
func TestPassportRevisionTransitions(t *testing.T) {
	checkTable(t, spawn.PassportRevisionMachine, []struct {
		path  []string
		event string
		want  string
	}{
		{nil, "validate", "validated"},
		{nil, "reject", "rejected"},
		{nil, "expire", "rejected"},
		{[]string{"validate"}, "activate", "active"},
		{[]string{"validate"}, "expire", "rejected"},
		{[]string{"validate", "activate"}, "supersede", "superseded"},
		{[]string{"validate", "activate"}, "retain", "active"},
		{[]string{"validate", "activate", "supersede"}, "activate", "active"},
		{[]string{"validate", "activate", "supersede"}, "retain", "superseded"},
		{[]string{"validate", "activate", "supersede"}, "prune", "pruned"},
	}, map[string][]string{
		"validated":  {"supersede", "reject", "validate", "retain", "prune"},
		"rejected":   {"validate", "activate", "expire", "supersede", "retain", "prune"},
		"active":     {"validate", "reject", "activate", "expire", "prune"},
		"superseded": {"validate", "reject", "supersede", "expire"},
		"pruned":     {"validate", "reject", "activate", "expire", "supersede", "retain", "prune"},
	})
}

// history events of id in seq order.
func history(t *testing.T, s *store.Store, m *fsm.Machine, id string) []string {
	t.Helper()
	var events []string
	err := s.Tx(t.Context(), func(tx *store.Tx) error {
		rows, err := tx.Scoped(tenant).Query("SELECT event FROM "+m.History+" WHERE {tenant} AND id = ? ORDER BY seq", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("history %s: %v", id, err)
	}
	return events
}

// An active revision is the space's only live policy: the sweeper must never
// tombstone it. Past every deadline the only event it fires is retain, which
// re-arms the bound and leaves the state untouched.
func TestActiveRevisionNeverExpires(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, migrations.FS)
		m := spawn.PassportRevisionMachine
		create(t, s, m, "r1")
		for _, event := range []string{"validate", "activate"} {
			if err := transition(t, s, m, "r1", event); err != nil {
				t.Fatal(err)
			}
		}
		past := t0.Add(365 * 24 * time.Hour)
		if _, err := fsm.Sweep(t.Context(), s, m, past, 10); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if state, _ := read(t, s, m, "r1"); state != "active" {
			t.Fatalf("active revision swept to %q — the space lost its live policy", state)
		}
		want := []string{"create", "validate", "activate", "retain"}
		got := history(t, s, m, "r1")
		if len(got) != len(want) {
			t.Fatalf("history %v, want %v", got, want)
		}
		for i, e := range want {
			if got[i] != e {
				t.Fatalf("history %v, want %v", got, want)
			}
		}
	})
}

// Rollback replaces v0 RestoreCurrent: a superseded revision stays
// activatable no matter how old it gets — the sweeper retains it, it never
// expires.
func TestSupersededRevisionStaysRollbackable(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		s := storetest.Open(t, url, migrations.FS)
		m := spawn.PassportRevisionMachine
		create(t, s, m, "r2")
		for _, event := range []string{"validate", "activate", "supersede"} {
			if err := transition(t, s, m, "r2", event); err != nil {
				t.Fatal(err)
			}
		}
		past := t0.Add(365 * 24 * time.Hour)
		if _, err := fsm.Sweep(t.Context(), s, m, past, 10); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if state, _ := read(t, s, m, "r2"); state != "superseded" {
			t.Fatalf("superseded revision swept to %q — rollback is gone", state)
		}
		if err := transition(t, s, m, "r2", "activate"); err != nil {
			t.Fatalf("rollback a year later: %v", err)
		}
		if state, _ := read(t, s, m, "r2"); state != "active" {
			t.Fatalf("rolled back revision is %q, want active", state)
		}
	})
}

// The runtime machine: absent is final for its generation (a claim creates
// the next generation's row); the sweep events are fail on the start
// budget, drain on the idle TTL and on a busy hold that outlives its bound,
// quarantine on the stop budget and removed on tombstone retention. The
// doc's "any" edges cover every live state: quarantine and missing leave
// every non-final state but their own target.
func TestRuntimeTransitions(t *testing.T) {
	checkTable(t, spawn.RuntimeMachine, []struct {
		path  []string
		event string
		want  string
	}{
		{nil, "ready", "warm"},
		{nil, "fail", "failed"},
		{[]string{"ready"}, "lease", "busy"},
		{[]string{"ready", "lease"}, "renew", "busy"},
		{[]string{"ready", "lease"}, "unlease", "warm"},
		{[]string{"ready", "lease"}, "drain", "draining"},
		{[]string{"ready"}, "drain", "draining"},
		{[]string{"ready", "drain"}, "stopped", "absent"},
		{nil, "quarantine", "quarantined"},
		{[]string{"ready"}, "quarantine", "quarantined"},
		{[]string{"ready", "lease"}, "quarantine", "quarantined"},
		{[]string{"ready", "drain"}, "quarantine", "quarantined"},
		{[]string{"fail"}, "quarantine", "quarantined"},
		{[]string{"missing"}, "quarantine", "quarantined"},
		{nil, "missing", "missing"},
		{[]string{"ready"}, "missing", "missing"},
		{[]string{"ready", "lease"}, "missing", "missing"},
		{[]string{"ready", "drain"}, "missing", "missing"},
		{[]string{"fail"}, "missing", "missing"},
		{[]string{"quarantine"}, "missing", "missing"},
		{[]string{"fail"}, "removed", "absent"},
		{[]string{"quarantine"}, "removed", "absent"},
		{[]string{"missing"}, "removed", "absent"},
	}, map[string][]string{
		"warm":        {"ready", "unlease", "stopped", "removed"},
		"busy":        {"ready", "lease", "stopped", "removed"},
		"draining":    {"ready", "lease", "unlease", "drain", "removed"},
		"failed":      {"ready", "lease", "stopped"},
		"quarantined": {"ready", "lease", "stopped", "quarantine"},
		"missing":     {"ready", "lease", "stopped", "missing"},
		"absent":      {"ready", "lease", "stopped", "removed"},
	})
}

// The worker machine is the runtime table without warm: a ready worker is
// leased straight away and a released or stuck worker drains instead of
// parking.
func TestWorkerTransitions(t *testing.T) {
	checkTable(t, spawn.WorkerMachine, []struct {
		path  []string
		event string
		want  string
	}{
		{nil, "ready", "busy"},
		{nil, "fail", "failed"},
		{[]string{"ready"}, "unlease", "draining"},
		{[]string{"ready"}, "drain", "draining"},
		{[]string{"ready", "drain"}, "stopped", "absent"},
		{nil, "quarantine", "quarantined"},
		{[]string{"ready"}, "quarantine", "quarantined"},
		{[]string{"ready", "drain"}, "quarantine", "quarantined"},
		{[]string{"fail"}, "quarantine", "quarantined"},
		{[]string{"missing"}, "quarantine", "quarantined"},
		{nil, "missing", "missing"},
		{[]string{"ready"}, "missing", "missing"},
		{[]string{"ready", "drain"}, "missing", "missing"},
		{[]string{"fail"}, "missing", "missing"},
		{[]string{"quarantine"}, "missing", "missing"},
		{[]string{"fail"}, "removed", "absent"},
		{[]string{"quarantine"}, "removed", "absent"},
		{[]string{"missing"}, "removed", "absent"},
	}, map[string][]string{
		"busy":        {"ready", "lease", "stopped", "removed"},
		"draining":    {"ready", "lease", "unlease", "drain", "removed"},
		"failed":      {"ready", "lease", "stopped"},
		"quarantined": {"ready", "lease", "stopped", "quarantine"},
		"missing":     {"ready", "lease", "stopped", "missing"},
		"absent":      {"ready", "lease", "stopped", "removed"},
	})
}

// Every state that can be left has a deadline whose event is a real edge
// out of it; every final state has none. fsm.New enforces this at build,
// this test keeps the promise legible per machine.
func TestMachinesHaveDeadlinePerNonFinalState(t *testing.T) {
	for name, m := range map[string]*fsm.Machine{
		"passport_revision": spawn.PassportRevisionMachine,
		"runtime":           spawn.RuntimeMachine,
		"worker":            spawn.WorkerMachine,
	} {
		exits := map[string]map[string]bool{}
		for _, e := range m.Edges {
			if exits[e.From] == nil {
				exits[e.From] = map[string]bool{}
			}
			exits[e.From][e.Event] = true
		}
		for state := range exits {
			d, ok := m.Deadlines[state]
			if !ok || d.After <= 0 {
				t.Errorf("%s: non-final state %q has no deadline", name, state)
				continue
			}
			if !exits[state][d.Event] {
				t.Errorf("%s: deadline event %q is not an edge out of %q", name, d.Event, state)
			}
		}
		for state := range m.Deadlines {
			if len(exits[state]) == 0 {
				t.Errorf("%s: final state %q has a deadline", name, state)
			}
		}
	}
}
