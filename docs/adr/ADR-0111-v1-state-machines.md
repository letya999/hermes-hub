---
description: v1 state machines - one Transition function in shared/fsm over a per-service table, history in the same transaction, a deadline and sweeper for every non-final state, uncertain never retried automatically, no stored flag beside a state.
last_verified: 2026-11-20
---
# ADR-0111: v1 state machines

Status: proposed (M40.1 #599, CHG-0334). Direction agreed with the owner on
2026-11-20; record text awaits owner review.
Delivery: M40.2 (`shared/fsm`, #600); each service issue declares its
machines. Catalog: [state-machines.md](../state-machines.md).
Builds on: ADR-0036 (uncertain reconciliation), ADR-0099 (connector slot
FSM, the first explicit table in v0).

## Context

The 2026-11-19 implicit-FSM audits traced v0 lifecycles spread across
status strings, booleans, counters, nullable fields and file locations. Ten
confirmed defects follow from that shape (M40.0, CHG-0333): states with no
exit (spool `uncertain`, supervisor `running` after a persist failure,
dreaming occurrence, first-apply recovery), lost updates (goal), a counter
beside a map (runtime leases), and a stored flag that contradicts the state
(paused assignment). v0 also logs illegal phase moves instead of refusing
them.

## Decisions

1. **One `Transition`.** Every state change goes through
   `Transition(tx, machine, id, event)` in `shared/fsm`, validated against
   the machine's table declared by the owning service. A forbidden event
   returns an error and writes nothing.
2. **History in the same transaction.** The state and a history row
   (from, event, to, actor, time) are written in one transaction.
3. **Deadline and sweeper.** Every non-final state has a deadline and names
   who acts next: runtime, owner or sweeper. `Sweep(tx, machine, now)`
   moves expired states along their table; nothing waits forever.
4. **`uncertain` is never retried automatically.** It leaves only by
   observation of the real outcome or by the owner.
5. **No stored flag beside a state.** Counts and booleans that follow from
   rows are derived (a lease count is the number of lease rows). A new
   column that shadows a state is refused in review.
6. **Conditional updates.** A write after a network call re-reads under the
   transaction and refuses if the state or epoch changed (closes lost
   updates).
7. **Documented.** Each machine is a table in its service README and in
   [state-machines.md](../state-machines.md).

## Enforcement

`TestTransitionRejectsForbiddenAndWritesNothing` and
`TestSweepMovesExpiredState` (M40.2); a table test per machine (allowed and
forbidden events); `TestUncertainNeverAutoRetried` (M40.6); the M40.0
regressions re-expressed as FSM tests in M40.3, M40.6 and M40.10.

## Alternatives

- **Workflow engine or statechart library.** Rejected: a table plus one
  function covers every machine in the catalog.
- **Status strings with per-site checks (v0).** Rejected: the audits show
  where that leads.

## Preserved v0 invariants

- Uncertain outcomes are never retried (ADR-0036).
- The connector slot keeps one live onboarding per owner and definition
  (ADR-0099).
