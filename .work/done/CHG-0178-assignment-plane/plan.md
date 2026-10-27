# CHG-0178: assignment plane (M23 stage 1)

Chain: `docs/adr/ADR-0082-standing-assignments.md` D1–D4 ->
`specs/active/SPEC-0080-standing-assignments.md` R1 -> this plan ->
`state.yaml`. Evidence: `research.md`.
Previous stage: —. Next stage: `CHG-0179` (acquisition), `CHG-0180` (surface).

## Goal

The owner can hand Hermes a durable standing instruction ("watch X, wake the
specialist when Y, report to Z") that survives restart, applies its plan
idempotently to routine artifacts, and can be paused/resumed/cancelled with
every queued, running or staged effect fenced at the effect point.

## Read before coding

1. `internal/commodel/routine.go` (Schedule, RoutineDraft, WatchProbe, Job),
   `internal/commodel/ids.go`, `internal/identity/identity.go` (Envelope).
2. `internal/routines/{engine,schedules,watchers,validate}.go` — store
   conventions (`atomicJSON`, `LoadScheduleLocked`, `CreateScheduleAt`,
   `PauseSchedule`, `DeleteSchedule`, `BlockScheduleLocked`,
   `OccurrenceCurrentLocked`, `ScheduleIntent`), the probeOne/settle
   pipeline and the `notify_mode=agent` enqueue seam.
3. `internal/communication/{spool,events,gateway,workers}.go` —
   `enqueueLocked`, `fanoutEventsLocked` (sink=agent), job claim,
   delivery claim.
4. `research.md` prior-art table.

## Preconditions and rules

- Red test first; engine stays a leaf (deps injected); stdlib only.
- Owner-only intent; full-envelope match on every mutation; stale
  `expected_revision` ⇒ revision error.
- No second scheduler/queue/process. Reuse spool, occurrence and outbox.

## Slices

### S1.1 Assignment record + store (R1.1, R1.2, R1.9)

Anchors: new `internal/commodel/assignment.go`;
new `internal/routines/assignments.go`;
`internal/routines/engine.go` `Deps`;
`internal/communication/spool.go` `openSpool` (dir list).

Change:
1. `commodel.Assignment`, `AssignmentPlan`, `AssignmentSource`,
   `SpecialistSpec`, `AssignmentGrants`, `AssignmentCheckpoint` per SPEC
   R1.1/R1.3; `ValidateAssignment`, `AssignmentIntentHash`.
2. Engine: `assignmentPath`, `LoadAssignmentLocked`, `SaveAssignmentLocked`,
   `ListAssignments`, `assignmentState*` helpers; dir `assignments` in
   `openSpool` + `InspectSpool` create list.
3. Create: validate → intent-hash lookup under lock → replay returns
   existing (200-class) with `replayed:true` marker to caller.

Tests (red first):
- `TestAssignmentCreateIdempotent`
- `TestAssignmentIntentOwnerOnly`
- `TestAssignmentRecordRoundTrip` (corrupt file ⇒ error, not coerce)

### S1.2 Plan reconcile (R1.3, R1.4, R1.8)

Anchors: `internal/routines/assignments.go` `ApplyAssignmentLocked`;
`internal/routines/schedules.go` `createSchedule`,
`CreationReplayLocked`, `DeleteSchedule`;
`internal/commodel/routine.go` `Schedule` (add `AssignmentID`,
`AssignmentRev`, `WakePrompt`; RoutineDraft unchanged — plan writes fields).

Change:
1. `ApplyAssignmentLocked(id, caller, now)`: load + CAS + validate plan;
   per source: deterministic id `asg-<aid>-<name>`, stamp
   `AssignmentID/AssignmentRev/WakePrompt`; if schedule exists and
   `ScheduleIntent` differs → delete + create (receipt `replaced`);
   exists same intent → keep (`kept`); missing → create (`created`).
   Orphaned owned schedules → delete (`removed`). Persist
   `applied map[name]receipt{schedule_id,intent_sha}`; on any source
   failure record partial receipts, state=`failed`, return error.
   Success ⇒ `applied_rev=plan.revision`, state=`active`.
2. `reconcileAssignmentsLocked(now)` invoked at head of `TickSchedules`:
   active/waiting assignments recompute `blocked` from link states
   (missing artifact ⇒ `artifact_missing:<name>`; schedule.BlockedReason
   propagated); flip `waiting`↔`active` accordingly.

Tests:
- `TestAssignmentPlanIdempotent` (A1.2)
- `TestAssignmentRestartResume` (A1.1 — apply, wipe in-memory, reopen
  spool, apply again ⇒ receipts reused)
- `TestAssignmentOrphanRemoved`, `TestAssignmentArtifactMissingWaits`

### S1.3 Lifecycle + fencing + budget (R1.5–R1.7)

Anchors: `internal/routines/assignments.go`
(`PauseAssignmentLocked`, `ResumeAssignmentLocked`, `CancelAssignmentLocked`,
`CompleteAssignmentLocked`);
`internal/routines/watchers.go` `SettleProbe` agent branch (~L373);
`internal/communication/events.go` `fanoutEventsLocked` sink=agent;
`internal/communication/workers.go` job claim;
`internal/communication/spool.go` `claimDeliveryLocked`;
`internal/commodel/{routine,delivery}.go` (`Job.AssignmentID`,
`Delivery.AssignmentID`).

Change:
1. Transitions per R1.5 (pause→pause links, resume→resume links,
   cancel→pause+delete links+tombstone, complete→stop scheduling keep
   record). CAS via `expected_revision`.
2. `assignmentGateLocked(schedule)` helper on engine:
   runnable? + `recordWakeLocked` budget count (day rollover).
   Wire into `SettleProbe` agent enqueue + `fanoutEventsLocked` agent
   sink: gate false ⇒ occurrence `cancelled`/`capped`, no enqueue.
3. `Job.AssignmentID`, `Delivery.AssignmentID` populated from schedule;
   worker claim drops non-runnable (`FinishJob` failed, reason logged);
   delivery claim drops staged deliveries of non-runnable assignments.
4. `WakePrompt` prepended to agent-sink job text before the
   `[untrusted …]` evidence line.

Tests:
- `TestAssignmentCancelFencesLateWake` (A1.3)
- `TestAssignmentPauseFencesQueued` (pause ⇒ queued wake job drops)
- `TestAssignmentWakeBudget` (A1.6)
- `TestAssignmentStaleRevisionDenied`
- `TestAssignmentWakePromptWrapsEvidence`

## Red-team checklist

- Agent-crafted plan naming a foreign conversation/chat ⇒ draft
  validation + envelope routing reject (identity never from text).
- Replay create with foreign principal ⇒ distinct intent hash ⇒ new
  record under caller envelope, never alias to victim's.
- Delete an owned schedule via routine_delete ⇒ reconcile ⇒ `waiting`
  with `artifact_missing`, wake path fenced.
- Pause between probe settle and outbox send ⇒ delivery claim drops.
- Corrupt `assignments/<id>.json` ⇒ load error; list skips it; no panic.
- Grant widen via API ⇒ refused (A1.5 lives in CHG-0180 surface tests;
  engine-level: `UpdatePlan` rejects `Grants` widening unconditionally —
  widen happens only through command-side record write after authorize).

## Acceptance

SPEC-0080 A1.1–A1.6 unit-level green; `go test ./internal/routines/
./internal/communication/ ./internal/commodel/` and `just check`.

## Out of scope

- REST/chat/tool surface (CHG-0180), acquisitions/filters/hooks
  (CHG-0179), live runs (CHG-0181), Slack specifics beyond WakePrompt +
  urgent flag plumbing.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml slices/validation/evidence updated.
- [ ] Commit `CHG-0178` slices.
