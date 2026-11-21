---
description: State-machine catalog - every lifecycle v1 services own (Job, Runtime, PassportRevision, ChannelLink, Routine/Occurrence, Assignment, Goal, ConnectorBinding, OwnerDecision); the v0 states and transitions in code today, the known defects M40.0 fixes, and the v1 target table.
last_verified: 2026-11-21
---

# State machines

One page per lifecycle the v1 services own. For each machine: where it lives
in v0 code today, the defects the 2026-11-19 audits confirmed (fixed in
place by M40.0, CHG-0333, issue #598), and the v1 target table that the
owning service implements with `shared/fsm` (rules F1-F6 in
[engineering.md](engineering.md#state-machine-rules),
[ADR-0111](adr/ADR-0111-v1-state-machines.md)).

v0 facts were read from the code on `feat/m40-v1-platform` on 2026-11-20.
Line numbers drift; function names are the stable anchors. The v1 target
tables are the starting point for the owning service issue, which may
refine them; when a service extracts a machine, its README gets the final
table and this page is updated in the same change.

| Machine | v1 owner | Milestone |
|---|---|---|
| [Job](#job) | task-engine | M40.6 |
| [Runtime](#runtime) | spawn-controller | M40.3 |
| [PassportRevision](#passportrevision) | spawn-controller | M40.3 |
| [ChannelLink](#channellink) | identity-hub | M40.5 |
| [Routine and Occurrence](#routine-and-occurrence) | proactive-engine | M40.10 |
| [Assignment](#assignment) | proactive-engine | M40.10 |
| [Goal](#goal) | proactive-engine | M40.10 |
| [ConnectorBinding](#connectorbinding) | tool-hub | M40.7 |
| [OwnerDecision](#ownerdecision) | tool-hub | M40.7 |

## Job

### v0 today

Three ledgers hold job state.

1. **Supervisor job** (`internal/supervisor`, `jobRecord`): `Status` is
   `running`, `uncertain`, `waiting_for_approval`, `completed`, `failed`,
   `cancelled`, `interrupted`. Terminal: completed, failed, cancelled,
   interrupted (`terminalRunStatus`). A parallel `Phase` (`phases.go`:
   accepted, waking, admitted, running, waiting_approval, completed,
   failed, cancelled, interrupted) has an edge table, but an illegal move is
   only logged, never refused. Writers: `beginJob`, `claimManagedLane`
   (lane: at most 5 active jobs per principal and context, one per
   conversation), `bindJobGeneration` (sets `RunDeadline`),
   `finishJobGenerationLocked`, `control.go` cancel and approval paths,
   restart load (`running` -> `uncertain`). Sweeper `Manager.sweep`:
   `ExpireApprovals`, `reconcileUnconfirmed` (fails after `RunBudget`),
   `ExpireUncertain` (`uncertain` -> `interrupted`), `CompactJobs`.
   Shadow fields: `Unconfirmed`/`UnconfirmedSince` duplicate `uncertain`;
   `Dispatching`, `CancelRequested`, `RuntimeMode` (the lane-held marker),
   a per-runtime `consecutiveUncertain` counter.
2. **Communication spool job** (`internal/communication`): state is the
   file's directory (`pending/`, `running/`, `done/`, `failed/`) plus
   `JobMapping.Status` (`accepted`, `running`, `uncertain`, `completed`,
   `failed`, `cancelled`, `interrupted`) plus the in-memory lane map.
   Writers: enqueue, `ClaimJob`, `CompleteJob`, `FailJob` (writes
   `uncertain` as non-terminal and keeps the lane), startup `recoverJobs`,
   `RequeueObservations`. No sweeper for `running/`; only a restart moves
   it. Shadow fields: `UncertainSince`, `ObservationAttempts`,
   `NextObservationAt`, `ApprovalState`, `Control.State`.
3. **ToolHub automation dispatch intent** (`internal/toolhub/automation`):
   state spread over `Acked`, `RetryStopped` and `Mapping.Status`. An
   `uncertain` supervisor result counts as not terminal, so the intent is
   re-submitted every tick with no local budget.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| Spool `uncertain` holds the conversation lane forever: after the uncertain notice, re-observation stops and the mapping keeps the lane | `communication/recovery.go` `RequeueObservations` | `TestUncertainJobReleasesLaneAfterSupervisorSettles` |
| Supervisor job stuck `running` with `Unconfirmed=false` after a fence or persist failure; every caller drops the error, no sweeper matches | `supervisor/execute.go` `finishJobGenerationLocked` | `TestRunningJobPastDeadlineBecomesUncertain` |
| Gateway leaves a claimed job in `running/` with the lane held on two `continue` paths | `communication/gateway_delivery.go` | `TestCorruptMappingSettlesClaimedJob` |

### v1 target (task-engine)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | create with idempotency key | queued | queue deadline; sweeper cancels |
| queued | lane free, rights current, runtime claimed | admitted | admit deadline; runtime |
| admitted | runtime confirms run start | running | run deadline; runtime |
| running | result recorded | succeeded or failed | final |
| running | tool evaluates to `ask` | waiting_owner | decision TTL; owner |
| waiting_owner | decision approved | queued | - |
| waiting_owner | decision denied or expired | cancelled | final |
| admitted, running | outcome unknown (lost ack, crash after effect, deadline passed) | uncertain | observation deadline; observer or owner |
| uncertain | observation proves the outcome, or owner resolves | succeeded or failed | final |
| queued, admitted, running | `/stop`, panic stop, revoke | cancelled | final |

`uncertain` is never retried automatically. Lanes are derived from
non-final rows, never stored.

## Runtime

### v0 today

`Runtime.State` in `internal/supervisor`: `Stopped`, `Starting`, `Ready`
(declared, never assigned), `Busy`, `Idle`, `Stopping`, `Degraded`. No
terminal state; `Stopped` can be ensured again. `Ensure` moves to
`Starting`/`Busy`, lease release to `Idle`, `Reap` to `Stopping`/`Stopped`,
`markDegraded` to `Degraded`. `Reap` handles only `Idle` and `Degraded`
with `IdleDeadline`; nothing bounds `Starting` or `Busy`. Shadow fields: a
`Leases` counter beside the `leases` map, `Desired`, `PendingRestart`,
crash counters.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| `Ensure` increments the `Leases` counter while `Acquire` inserts the map entry in a second persist; a failure between them pins the runtime `Busy` forever | `supervisor/ensure.go` `Ensure`, `supervisor/lease.go` `Acquire` | `TestLeaseCountDerivedFromLeaseRows` |

### v1 target (spawn-controller)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | claim for an admitted job | starting | start budget 30s, deadline `fail`; backend probe, sweeper backstop |
| starting | `ready` (probe passes) | warm | idle TTL 15m, deadline `drain`; sweeper |
| starting | `fail` (budget exceeded or probe fails) | failed | retention 24h, deadline `removed`; sweeper (operator or reconciler) |
| warm | `lease` (lease row created) | busy | hold bound 2h, deadline `drain`; sweeper |
| busy | `renew` (lease heartbeat) | busy | re-arms the 2h hold bound |
| busy | `unlease` (last lease row removed) | warm | idle TTL 15m, deadline `drain`; sweeper |
| warm | `drain` (idle TTL or policy digest revoked) | draining | stop budget 30s, deadline `quarantine`; backend confirms stop, sweeper backstop |
| busy | `drain` (hold bound passed) | draining | stop budget 30s, deadline `quarantine`; sweeper |
| draining | `stopped` (inspect confirms stop) | absent | final for this generation |
| any non-final | `quarantine` (stop not confirmed) | quarantined | retention 24h, deadline `removed`; sweeper (operator) |
| any non-final | `missing` (inspect finds nothing) | missing | retention 24h, deadline `removed`; sweeper (reconciler) |
| failed, quarantined, missing | `removed` (removal confirmed) | absent | final |

Every `starting` increments the generation (`gen` in run tokens). The
lease count is the number of lease rows. Worker follows the same table
without `warm`: `ready` lands `busy`, `unlease` and `drain` both leave
`busy` for `draining`.

S4 implementation notes (`services/spawn-controller/internal/spawn`,
verified 2026-11-21): `absent` is final per generation — a claim creates
a new-generation row in `starting`, it is not an edge out of `absent`.
"any" fans out to every non-final state except the edge's own target. A
`busy` hold that outlives its bound drains — the timed-out hold's runtime
is reclaimed, not returned dirty to the warm pool. `renew` is the lease
heartbeat (`POST /v1/leases/{id}/renew`): a `busy` self-loop that re-arms
the hold bound, so an actively renewed hold never drains. Every move out
of the leasable states (`warm`, `busy`) deletes the runtime's lease rows
in the same transaction — a draining, missing or quarantined row can
never carry a hold into its next generation. The claim binds tenant,
policy digest and image digest to the row (Decision 10): warm reuse is
exact-key only, and activation drains every runtime bound to the old
digest in the activation transaction. A runtime whose backend Start
outlives an activation is the gap activation cannot see — its ready
move checks the space's stored digest in the same transaction and warms
straight into draining on a mismatch, the claim re-plans for the current
digest, and the sweeper drains any warm/busy row whose digest stops
matching as the backstop for both.

### v1 live (spawn-controller)

S4 (M40.3) wires the durable target table to HTTP: `RuntimeMachine`
drives the `runtimes` rows the message path claims — `POST /v1/messages`
is claim -> acquire lease -> forward -> release, `GET /v1/runtimes`
lists the tenant's rows, `POST /v1/leases/{id}/renew` fires `renew`. The
sweeper (`HUB_SWEEP_INTERVAL`, default 5s) fires the deadline events and
then, outside any transaction, applies the backend effects: `draining`
rows get a confirmed `Stop` before `stopped`, an unconfirmed stop stays
for the `quarantine` deadline, and a `failed` row's half-started workload
is stopped. The same pass reconciles rows against a successful backend
`List` — absent workloads become `missing` and labeled workloads without
rows (orphans of a crashed controller) are stopped; a `List` error marks
nothing.

Children are bound to a process group (Unix) or Job Object (Windows) so
a controller death cannot orphan them; a restarted controller recovers
its runtimes from the rows and reconciles the backend at boot before
serving.

S7 (M40.3) moved the consumer side: the supervisor's `Ensure` claims
through `contracts/spawn/v1` (`ClaimRuntime` returns the row's `address`
plus a lease), `Reap` releases the lease, restart/respawn/remediation
all drain and re-claim, and a supervisor restart rediscovers its runtime
by renewing the persisted lease rather than cold-claiming. `POST
/v1/spaces/{space}/runtimes/drain` is the respawn/replace surface. The
`docker` backend starts managed runtimes on the internal agent network
with the dual-homed control relay as sole inbound route; `process`
serves trusted dev.

## Worker

### v0 today

The ToolHub CLI cell (`internal/toolhub` `cell*.go`): an in-memory warm
registry keyed on image and warm key — `claimPoolCell`, `returnPoolCell`,
`execInCell`, `cellRegistry`, `execWarm`. No durable row, no lease: a
ToolHub restart forgot live cells and orphaned their containers, the
cross-user pool let one tenant's worker serve another, and drift was
checked only when a cell was reused, never reconciled. Config keys
`pool_size` and `pool_claim_wait_seconds` tuned the in-memory pool.

### Defects fixed by M40.3 (S6)

| Defect | Location | Resolution |
|---|---|---|
| Worker state lived in memory: restart orphaned running cells and lost who claimed what | `internal/toolhub/cell_lifecycle.go` | workers are durable `workers` rows under `WorkerMachine`; the sweeper reconciles |
| Cross-user warm pool could serve a foreign tenant's exec | `execPooled`/`claimPoolCell` | claim binds tenant + space + cell key + image digest; pool config deleted |
| Reused cells trusted the spec they were started with | `execWarm` | reuse re-runs `Cells.spec` + `Backend.VerifyWorker` + canary/procs probes; drift drains and re-plans |

### v1 target (spawn-controller)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | claim for a cell key | starting | start budget 30s, deadline `fail`; backend probe, sweeper backstop |
| starting | `ready` (verify probes pass) | busy | hold bound 2h, deadline `drain`; lease `renew` re-arms it |
| starting | `fail` | failed | retention 24h, deadline `removed`; sweeper |
| busy | `renew` (lease heartbeat) | busy | re-arms the lease bound |
| busy | `unlease` (release) or `drain` | draining | stop budget 30s, deadline `quarantine`; backend confirms stop |
| draining | `stopped` (inspect confirms stop) | absent | final for this generation |
| any non-final | `quarantine` / `missing` | quarantined / missing | retention 24h, deadline `removed` |
| failed, quarantined, missing | `removed` | absent | final |

Differences from Runtime: **no `warm`** — release drains, a worker is
never returned to a reusable pool, and a re-claim mints the next
generation. The claim binds tenant, space, cell key and image digest, so
an executor's worker can never serve a foreign space or a superseded
image.

### v1 live (spawn-controller)

S6/S7 (M40.3) wired `WorkerMachine` to the contract: `POST
/v1/spaces/{space}/workers` claims (org+owner tenant, space, cell key,
image digest -> `starting` -> `busy` row plus a lease), `POST
/v1/workers/{id}/exec` is lease-guarded bounded exec (env delivered by
name through `docker exec -e`, never in argv; timeout and output caps
enforced), `/v1/workers/{id}/renew` and `/release` drive the lease
edges, and `POST /v1/spaces/{space}/workers/drain` filtered by
binding/job backs ToolHub's `/cli-release`. The worker row carries the
sanitized claim (brokered credential values stripped) and the reuse
guard re-verifies profile before serving. Worker lease rows carry a 30m
TTL — `renew` extends only the caller's hold, and the sweeper unleases a
worker whose holds all expired into `draining`. ToolHub's `execCLI` claims,
renews and releases through `contracts/spawn/v1`; the old cell files and
the in-memory pool were deleted in the same slice (S2/S4 move-then-
delete rule). `TestWorkerTransitionsWired`,
`TestWorkerMountsOnlyOwnSpace`, `TestWorkerNeverCrossesTenants` and
`TestExecNoSecretInArgv` cover the machine; `TestWorkerCellRealDocker`
runs the real docker leg.

## PassportRevision

### v0 today

Three pointers: the file `generated/passports/current` (a revision id, a
policy digest, `pending`, or absent; `internal/passport/publish.go`
`Prepare`, `Activate`, `PointCurrent`, `RestoreCurrent`), the apply journal
`apply-journal.json` with phases `preparing`, `prepared`, `rendered`,
`rights`, `activated`, `binding`, `effects` (`cmd/hubctl/space_apply.go`),
and the identity-hub row `active_passport_bundles` (compare-and-set). No
deadline: recovery runs only on the next apply or settings edit. The
journal's presence acts as an "apply in progress" flag that readers fence
on.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| A first apply that crashes in `effects` always ends in "needs intervention" and keeps the journal, so every later apply fails | `cmd/hubctl/space_apply_recovery.go` `recoverApplyWithRebuiltImage` | `TestFirstApplyCrashRecoversWithoutIntervention` |
| Render-time binding migration silently skipped for non-infra spaces after the SQLite cutover; the store path lookup is copied in six places | `cmd/hubctl/render.go` `migrateToolhubBindings` | `TestRenderMigratesBindingsOnSQLiteStore` |

### v1 target (spawn-controller)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | passports submitted for a layer | draft | draft TTL 10m, deadline `expire`; sweeper removes |
| draft | `validate` (compiler validates) | validated | activation window 1h, deadline `expire`; sweeper |
| draft | `reject` (compiler rejects) | rejected | final |
| draft | `expire` (draft TTL passed) | rejected | final |
| validated | `activate` (compare-and-set on the current active) | active | bound 30d, deadline `retain` (self-loop re-arm); owner activates a newer revision |
| validated | `expire` (never activated) | rejected | final |
| active | `supersede` (a newer revision activates) | superseded | bound 30d, deadline `retain` (self-loop re-arm); owner rolls back |
| superseded | `activate` (rollback activates it) | active | bound 30d, deadline `retain` (self-loop re-arm) |
| superseded | `prune` (retention count passed) | pruned | final |
| active, superseded | `retain` (bound passed, nothing else acted) | same state | re-arm only; `active` and `superseded` never expire |

Rollback is activation of an older revision. Readers fetch a slice by
digest; there is no `pending` pointer and no journal file.

S2 implementation notes (`services/spawn-controller/internal/spawn`,
verified 2026-11-20): `active` is the space's only live policy and
`superseded` is the rollback target (v0 `RestoreCurrent`), so the sweeper
must never tombstone either — the `retain` self-loop only re-arms their
bound and writes history. Pruning is the explicit count-based edge
`superseded --prune--> pruned`: the activation transaction keeps the
newest `passport.RevisionKeepSlack` (3) superseded revisions per
(space, layer) and moves older ones to the final `pruned` state.
`rejected` and `pruned` are the tombstones. `rejected`/`pruned` content
may be resubmitted — the digest dedup only binds non-pruned rows.
Activation is a set operation: one transaction takes one or more
revisions (at most one per layer) and first compiles the would-be active
set — a set that cannot compile writes nothing, so a space never loses
its serving policy to a partial cut; bootstrap is one activation of all
layers together.

### v1 live (spawn-controller)

S2/S7 (M40.3) run the machine end to end on the contract: `POST
/v1/spaces/{space}/layers/{layer}/revisions` submits a `draft`,
`/revisions/{id}/validate` compiles it into `validated` or `rejected`,
`/activations` cuts a validated set in one transaction, and `POST
/v1/spaces/{space}/apply` folds submit+validate+activate into one call
on the `base_digest` compare-and-swap — a stale base gets `conflict`,
never a silent overwrite (`TestActivateRefusesStaleBaseDirect`,
`TestActivateConcurrentBaseRace`). `POST /v1/spaces/{space}/settings`
applies one allowlisted config leaf through the same CAS; rollback is
activation of a `superseded` revision (`TestRevisionRollbackActivatesOlder`),
and a failed activation leaves the previous active set serving
(`TestApplyCrashLeavesPreviousActive`). Every activation stores the new
effective digest on the space row and drains runtimes bound to the old
digest in the same transaction (`TestRevokeDrainsOldDigest`).

The v0 file triplet is gone (S7b/S7d): `generated/passports/current`,
`.policy/<hex>` links, `apply-journal.json` and `internal/passportview`
are deleted — consumers read `GET /v1/spaces/{space}/effective` or the
immutable `GET /v1/spaces/{space}/policy/{service}?digest=` slice
(`ETag` = digest, `Cache-Control: immutable`), and `apply` no longer
keeps a journal; the activation transaction is the crash boundary.
`GET /v1/spaces/{space}/explain` serves the stored per-leaf layer
attribution. The v0 importer (`HUB_IMPORT_V0_DIR`) projects existing
spaces into `spaces` + `active` user-layer revisions through the same
Submit/Validate/Activate path.

## ChannelLink

### v0 today

`links.state` in `internal/identityhub` with a CHECK on `pending`,
`verified`, `revoked`, `gone` and a unique index on verified links. This is
the one v0 machine with an explicit table (`linkTransitions`) and a single
writer (`transitionLink`). Edges: pending -> verified (`verify`), pending
-> gone (`reject`, `expire`), verified -> revoked (`revoke`), verified ->
gone (`delete`), verified -> verified (`prove_oidc`). Sweeper:
`ExpirePendingLinks` on `expires_at`. Gap: `BeginLink`/`VerifyLink` (the
in-channel proof path) have only test callers; production paths
(migration, OIDC callback, shared spaces) insert `pending` and verify at
once. The Telegram connector login (`communication/telegram_auth.go`) is a
separate in-memory flow with a 10-minute TTL, not a link.

### v1 target (identity-hub)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | owner starts a link (code, OIDC callback, operator import) | pending | proof TTL; sender |
| pending | observed sender echoes the code in the channel | verified | - |
| pending | proof rejected or TTL passes | gone | final |
| verified | owner or operator revokes | revoked | final |
| verified | principal deleted or deactivated | gone | final |

An OIDC callback alone leaves the link `pending` (M40.5,
`TestChannelLinkRequiresInChannelProof`).

## Routine and Occurrence

### v0 today

A routine (`commodel.Schedule`, `internal/routines`) has no state field;
`ScheduleState` derives `blocked`, `completed`, `paused` or `active` from
`BlockedReason`, `Enabled`, `NextDue` and `Paused`. An occurrence
(`RoutineOccurrence.State`) takes `pending`, `probing`, `scripting`,
`dreaming`, `dispatched`, `cancelled`, `missed`, `blocked`, `failed`,
`capped`, `no_change`, `skipped`. `pending` becomes `missed` after the
catch-up window; check probes retry up to `MaxRetries`; `scripting` and
`dreaming` have no budget. ToolHub automation keeps per-stream event state
with a `Pending` batch, and a dreaming day record `pending -> prepared ->
complete`.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| A nightly dreaming occurrence that errors stays `dreaming`; the oldest occurrence per schedule is picked first, so no later night runs | `routines/system_dreaming.go` `RunDueDreaming` | `TestFailedDreamingNightDoesNotBlockNextNight` |
| A pending event batch survives a schedule edit; `resume` refuses it forever and the error aborts every stream of the principal | `toolhub/automation/host.go` `tickStreams`, `ports.go` `resume` | `TestScheduleEditDropsPendingBatchAndKeepsOtherStreams` |

### v1 target (proactive-engine)

Routine:

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | owner creates (agent: draft only) | draft | draft TTL; owner |
| draft | owner confirms | active | next slot; clock |
| active | owner pauses | paused | - |
| paused | owner resumes | active | - |
| active | grant revoked or validation fails | blocked | owner |
| blocked | owner edits and revalidates | active | - |
| active | end of schedule | completed | final |
| any non-final | owner deletes | cancelled | final |

Occurrence (id = hash of tenant, routine, slot):

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | slot due | pending | catch-up window; clock |
| pending | catch-up window passed | missed | final |
| pending | deterministic kind runs (probe, script) | running | attempt budget; worker |
| pending | agent kind submits a job to task-engine | dispatched | job deadline; task-engine |
| running | result with change | succeeded | final |
| running | result without change | no_change | final |
| running, dispatched | attempt limit or deadline reached | failed | final |
| pending | routine paused, blocked or cancelled | cancelled | final |

A failed occurrence never blocks the next slot.

## Assignment

### v0 today

`Assignment.State` (`routines/assignments.go`): `draft`, `waiting`,
`active`, `paused`, `completed`, `cancelled`; `failed` is listed in the
field comment but never set. Writers: `ApplyAssignment` (-> active),
`UpdateAssignmentPlan` and `ReconcileAssignments` (-> waiting),
`PauseAssignment`, `ResumeAssignment`, `CompleteAssignment`, cancel.
Terminal: completed, cancelled. No deadline. Shadow fields: `Blocked`,
`AppliedRev` against `Plan.Revision`, a `Paused` flag on each owned
schedule.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| Applying or re-planning a paused assignment marks it active or waiting while its schedules stay paused; resume then refuses | `routines/assignments.go` `ApplyAssignment`, `UpdateAssignmentPlan` | `TestApplyRefusesPausedAssignment` |

### v1 target (proactive-engine)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | owner creates (agent: draft only) | draft | draft TTL; owner |
| draft | owner applies the plan | active | assignment deadline; clock |
| active | a required artifact is missing or a plan revision is pending | waiting | owner |
| waiting | owner applies the plan | active | - |
| active, waiting | owner pauses | paused | - |
| paused | owner resumes | active | - |
| active | owner or evidence marks it complete | completed | final |
| active | deadline passes | failed | final |
| any non-final | owner cancels or grant revoked | cancelled | final |

Apply refuses `paused`; a plan update on a paused assignment is staged —
the plan revision advances but nothing is reconciled until `resume`, which
applies the staged plan (new schedules born paused) and then unpauses the
whole set. The owned schedules' paused state is derived from the
assignment, not stored on each schedule.

## Goal

### v0 today

`Goal.Status` (`communication/goal.go`): `active`, `paused`, `done`,
`blocked`, `stopped` (set in code but missing from the field comment), plus
whatever the native engine mirrors. Clear deletes the file. Set bumps
`Epoch`; continuation jobs carry the epoch and stale out on preemption. No
terminal state is enforced (set or resume revives any status) and no
deadline exists; `MaxTurns` is a turn budget. The runtime goal judge is
stateless (`continue`, `done`, `blocked`). Shadow fields: `Epoch`,
`TurnsUsed`, `ConsecTransport`, `ConsecParse` (never written),
`LastVerdict`.

### Defects fixed by M40.0

| Defect | Location | Regression test |
|---|---|---|
| `goalAfterJob` reads the goal, waits on the runtime, then saves without a compare-and-set, undoing a concurrent clear, pause or epoch bump | `communication/gateway_goal.go` `goalAfterJob` | `TestGoalClearDuringEvaluationIsNotUndone` |

### v1 target (proactive-engine)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | owner sets a goal (epoch + 1) | active | turn budget; runtime |
| active | judge says continue | active | next turn job |
| active | judge says done with evidence | done | final |
| active | judge says blocked | blocked | owner |
| active | owner pauses or a run fails | paused | owner |
| paused, blocked | owner resumes | active | - |
| active | turn budget exhausted | stopped | final |
| any non-final | owner clears | cleared | final |

Every write after the judge call is a conditional update on epoch and
status (ADR-0111 D6).

## ConnectorBinding

### v0 today

Onboarding follows ADR-0099 (`internal/toolhub/api/onboarding_stage.go`):
stages `pending`, `live`, `failed`, `gone`, plus one gate slot
(`confirm_setup`, `credentials`, `oauth`, `remove`) with nonce and expiry.
Edges are enforced by `ValidateOnboardingTransition`, but direct `Stage`
writes are spread over several control files; a revoked `gone` record can
return to `pending`. `LifecyclePass` refreshes expired confirm gates and
retries admission; nothing ages `pending` to `failed`. `Connection`,
`ToolBinding` and `WorkloadInstance` share one `api.Status` vocabulary
(active, disabled, revoked, deleting, degraded, reauth, starting, running,
stopped, failed, expired) with no transition table and about 30 direct
assignments. Shadow fields include `Tombstone`, `SupersededBy`,
`ConfirmationUsed`, `AdmissionPending`, `Finalizers` and retry counters.

### v1 target (tool-hub)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | owner starts setup | pending | setup TTL; owner (gate) |
| pending | gate satisfied and admission passes | live | - |
| pending | setup TTL passes or admission refused | failed | owner |
| failed | owner retries | pending | - |
| live | credentials expire or broker reports reauth | pending | owner (gate) |
| live, pending, failed | owner removes or grant revoked | removing | finalizer deadline; sweeper |
| removing | every finalizer confirmed | gone | final |

The gate slot keeps ADR-0099's rule: one live onboarding per owner and
definition. Finalizer progress is rows, not a flag beside the stage.

## OwnerDecision

### v0 today: five mechanisms

| Mechanism | Where | State today |
|---|---|---|
| Human and onboarding confirmation | `api.HumanConfirmation`, `toolhub/human_confirmation.go`, chat button in `communication/human_confirmation.go` | an armed `confirm_setup` gate; `ConfirmationUsed=true` after use; gate refreshed on expiry |
| Operator confirmation digests | `api.Confirmation`, `api.Confirm`, `hubctl --confirm` | no machine: a valid digest is the authority |
| Grant request acknowledgement | `ToolGrant.Status` `pending -> active`, `revoked`; `RequestToolGrant`, `AcknowledgeToolGrant` | expiry never stored, checked on read |
| Memory proposal approval | `cardProposal` in `toolhub/memory_proposal.go` | `""`, `applying`, `complete` plus a `Used` flag; denial only journaled |
| Tool-run approval | supervisor `waiting_for_approval` with a 2-minute deadline; gateway `ApprovalState`, `PendingControl.State` | `pending`, `uncertain`, `closed` |

### v1 target (tool-hub)

| From | Event | To | Deadline / next actor |
|---|---|---|---|
| - | a call evaluates to `ask` | pending | TTL; owner |
| pending | owner approves through a verified channel or `?t=` link | approved | use TTL; runtime |
| pending | owner denies | denied | final |
| pending | TTL passes | expired | final |
| approved | the bound call runs | used | final |
| approved | owner revokes or use TTL passes | revoked | final |

Bound to `sha256(tool + canonical args)`, single use; timeout means deny
([ADR-0109](adr/ADR-0109-action-modes-owner-decisions.md)). Tests (M40.7):
`TestApprovalSingleUseAndBoundToArgs`, `TestApprovalTimeoutDenies`,
`TestToolResultCannotApprove`.
