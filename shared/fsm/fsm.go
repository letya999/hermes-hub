// Package fsm is the one state-transition function of every v1 service: a
// per-service transition table, state and history written in the caller's
// transaction, and a deadline for every non-final state that Sweep enforces.
// State lives in one column; no flag beside it.
//
// The entity table carries org, owner, id, state and deadline (unix seconds,
// NULL in final states); the history table carries org, owner, id, seq, at,
// from_state, event and to_state. Both are tenant-scoped through shared/store.
package fsm

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/letya999/hermes-hub/shared/store"
)

var (
	// ErrForbiddenTransition refuses an event the table does not allow from
	// the current state; it also matches store.ErrConflict.
	ErrForbiddenTransition = errors.New("fsm: forbidden transition")
	// ErrStale reports that the state changed between read and write; it
	// also matches store.ErrConflict.
	ErrStale = errors.New("fsm: state changed concurrently")
	// ErrMachine refuses an invalid transition table.
	ErrMachine = errors.New("fsm: invalid machine")
)

// Edge allows Event to move an entity From one state To another.
type Edge struct {
	From, Event, To string
}

// Deadline bounds a non-final state: After that long in it, Sweep fires Event.
type Deadline struct {
	After time.Duration
	Event string
}

// Machine is one service's transition table for one entity kind.
type Machine struct {
	Table     string
	History   string
	Initial   string
	Edges     []Edge
	Deadlines map[string]Deadline

	next  map[string]map[string]string
	valid bool
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// New validates m: identifiers, unique edges, an initial state with an exit,
// and exactly one deadline for every non-final state whose event is an edge
// out of that state.
func New(m Machine) (*Machine, error) {
	if !identifier.MatchString(m.Table) || !identifier.MatchString(m.History) {
		return nil, fmt.Errorf("%w: table names", ErrMachine)
	}
	m.next = map[string]map[string]string{}
	for _, e := range m.Edges {
		if e.From == "" || e.Event == "" || e.To == "" {
			return nil, fmt.Errorf("%w: empty edge %+v", ErrMachine, e)
		}
		if m.next[e.From] == nil {
			m.next[e.From] = map[string]string{}
		}
		if _, dup := m.next[e.From][e.Event]; dup {
			return nil, fmt.Errorf("%w: duplicate edge %s/%s", ErrMachine, e.From, e.Event)
		}
		m.next[e.From][e.Event] = e.To
	}
	if len(m.next[m.Initial]) == 0 {
		return nil, fmt.Errorf("%w: initial state %q has no exit", ErrMachine, m.Initial)
	}
	for state := range m.next {
		d, ok := m.Deadlines[state]
		if !ok || d.After <= 0 {
			return nil, fmt.Errorf("%w: non-final state %q has no deadline", ErrMachine, state)
		}
		if _, ok := m.next[state][d.Event]; !ok {
			return nil, fmt.Errorf("%w: deadline event %q is not an edge out of %q", ErrMachine, d.Event, state)
		}
	}
	for state := range m.Deadlines {
		if len(m.next[state]) == 0 {
			return nil, fmt.Errorf("%w: final state %q has a deadline", ErrMachine, state)
		}
	}
	m.valid = true
	return &m, nil
}

func (m *Machine) deadline(state string, now time.Time) any {
	if d, ok := m.Deadlines[state]; ok {
		return now.Add(d.After).Unix()
	}
	return nil
}

func (m *Machine) check() error {
	if m == nil || !m.valid {
		return fmt.Errorf("%w: build it with fsm.New", ErrMachine)
	}
	return nil
}

// Create inserts id in the initial state with its deadline and the first
// history row.
func Create(q store.Scoped, m *Machine, id string, now time.Time) error {
	if err := m.check(); err != nil {
		return err
	}
	if err := q.Insert(m.Table, []string{"id", "state", "deadline"}, id, m.Initial, m.deadline(m.Initial, now)); err != nil {
		return err
	}
	return q.Insert(m.History, []string{"id", "seq", "at", "from_state", "event", "to_state"}, id, 1, now.Unix(), "", "create", m.Initial)
}

// State reads the current state of id; a missing or foreign id is
// store.ErrNotFound.
func State(q store.Scoped, m *Machine, id string) (string, error) {
	if err := m.check(); err != nil {
		return "", err
	}
	var state string
	err := q.QueryRow("SELECT state FROM "+m.Table+" WHERE {tenant} AND id = ?", id).Scan(&state)
	return state, err
}

// Transition applies event to id: the edge must exist from the current state,
// the state and its new deadline are written by compare-and-swap and one
// history row is appended, all in q's transaction. A refused event writes
// nothing.
func Transition(q store.Scoped, m *Machine, id, event string, now time.Time) error {
	from, err := State(q, m, id)
	if err != nil {
		return err
	}
	to, ok := m.next[from][event]
	if !ok {
		return fmt.Errorf("%w: %w: %q from %q", ErrForbiddenTransition, store.ErrConflict, event, from)
	}
	result, err := q.Exec("UPDATE "+m.Table+" SET state = ?, deadline = ? WHERE {tenant} AND id = ? AND state = ?", to, m.deadline(to, now), id, from)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: %w: %s left %q", ErrStale, store.ErrConflict, id, from)
	}
	var seq int
	if err := q.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM "+m.History+" WHERE {tenant} AND id = ?", id).Scan(&seq); err != nil {
		return err
	}
	return q.Insert(m.History, []string{"id", "seq", "at", "from_state", "event", "to_state"}, id, seq+1, now.Unix(), from, event, to)
}

// Sweep fires the deadline event of every entity whose deadline passed and
// returns how many moved. It reads only tenant keys across tenants; each
// transition is scoped to the row's own tenant.
func Sweep(tx *store.Tx, m *Machine, now time.Time) (int, error) {
	if err := m.check(); err != nil {
		return 0, err
	}
	type due struct{ org, owner, id, state string }
	rows, err := tx.QueryAllTenants("SELECT org, owner, id, state FROM "+m.Table+" WHERE deadline IS NOT NULL AND deadline <= ?", now.Unix())
	if err != nil {
		return 0, err
	}
	var expired []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.org, &d.owner, &d.id, &d.state); err != nil {
			_ = rows.Close()
			return 0, err
		}
		expired = append(expired, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	moved := 0
	for _, d := range expired {
		deadline, ok := m.Deadlines[d.state]
		if !ok {
			continue
		}
		if err := Transition(tx.Scoped(store.Tenant{Org: d.org, Owner: d.owner}), m, d.id, deadline.Event, now); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}
