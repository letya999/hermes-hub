---
description: Frozen granular automation preparation, single-owner cutover and dreaming contracts.
last_verified: 2026-11-05
---
# SPEC-0099: M32 automation slices

Frozen: 2026-11-05. Implementation pending.
Decision: [ADR-0097](../../docs/adr/ADR-0097-m32-bounded-delivery.md).
Current worker contract for #521–#525; read assigned row plus invariants.
Trace: SPEC-0094 R3/R4/A3/A4/A5; no ownership/security requirement is removed.

## Owner and lifecycle contract

Target owner: ToolHub owns routine/occurrence/assignment/watch/digest state,
per-user serialization, clock, recovery, authorization and supervisor dispatch.
Communication owns channel validation/intake, delivery outbox and channel receipts.
There is one writer/clock at deployment; no new service/queue/database.
Dispatch has stable principal/environment/occurrence identity, durable claim and
idempotency key. Persist pending work before I/O; retry/reconcile uncertain outcome.
Current rights and pause/revoke fences apply. Completion and delivery are separate.
Communication outage may delay delivery, not admitted execution. Per-user bounds
prevent starvation; no network I/O while holding a shared global spool lock.

## Atomic assignments and observable checkpoints

| Issue | Deliverable and anchors | Acceptance / regression | Excluded |
|---|---|---|---|
| #521 A1 | Detach internal/routines engine from communication spool mutex and locked callbacks in Deps/NewEngine/spool.go. Engine-owned synchronization and explicit storage/dispatch/delivery/event ports; adapt current callers. | Same public routine/assignment/watch behavior and record format under current sole owner. Reentrant/concurrent tick, CRUD, recovery and completion do not deadlock, lose updates or double-dispatch; race suite. Real storage, no empty ports. | ToolHub startup, moving live state, new scheduler, dreaming. |
| #522 A2 | After A1 and #514: construct complete ToolHub automation host with existing storage and real supervisor/delivery clients. Include routine CRUD, assignments/watches/events, clock lifecycle, recovery and result/delivery intents. Do not start it from production entrypoint yet. | Isolated host using real temporary storage survives restart; cross-user and revoked operations denied; duplicate/lost-ack dispatch recovers to one accepted job; communication unavailable leaves delivery pending. Integration harness exercises real handlers/clients. | Production owner switch, duplicated state, rollout compatibility flag or claiming live readiness. |
| #523 A3 | Wire A2 into ToolHub startup and all public callers; stop/delete old communication writer/clock/dispatch startup in the same changeset. Reassign storage ownership without converting records or copying/syncing live state. | Real containers: CRUD + assignment/watch transitions use ToolHub; exactly one owner after restart; due work completes with communication down; delivery retries use stable request ID. Existing supported records retained; unsupported layout refuses without deletion. | New scheduler semantics, data migration, restoring old owner as fallback. |
| #524 A4 | Protected per-user daily dreaming routine from config.memory time/timezone, using A3 scheduler. | One routine per enabled user; ordinary CRUD cannot mutate it; local-date key stable over restart/timezone/DST handling. No model wake or vault write when disabled/ungranted. | Night computation/commit or a second timer. |
| #525 A5 | A4 executes internal/memory/night with user's vault/model relay and existing retention; durable checkpoints and atomic guarded commit. | Restart and duplicate attempts apply (user, local_date) once; revoke/disable before commit leaves no mutation. Digest request durable/idempotent and independent of completion. Proposed actions remain drafts until verified owner approval. | New model/provider, automatic approved assignments, external exactly-once promise. |

A1/A2 are preparation increments, not temporary dual ownership. A3 is the only
deployment switch. A2 does not introduce a production feature flag or second clock.
State ownership includes all assignment/watch/event writers, not just CRUD handlers.
Lost provider acknowledgments are reported honestly; idempotent hub requests alone
cannot prove exactly-once delivery by an external channel.

## Delivery gates

A1 can be assigned from the verified common `dev` SHA with #509/#513 in
separate lane worktrees.
A2 requires #521/#514. A3 requires #522. A4 requires #523/#516.
A5 requires #524/#518. Existing memory disable/operation grants apply at admission
and commit. Localized text, allowlisted audit and tool contract bundles remain.
Run just check and applicable AGENTS.md gates; live #526 includes two users,
gateway downtime, competing claims, restart, revoke and separate delivery evidence.
