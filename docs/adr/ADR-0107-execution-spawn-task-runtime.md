---
description: v1 execution - spawn-controller as the single container authority with a warm pool keyed by image digest, task-engine with one job ledger and conversation queues, harness-runtime with session slots and only its own workspace.
last_verified: 2026-11-20
---
# ADR-0107: Execution - spawn-controller, task-engine, harness-runtime

Status: proposed. Aligned 2026-11-20 with the simplified v1 plan (M40.1
#599, CHG-0334); the 2026-11-19 text (instant-space pods, instance pool,
execution classes, browser tab leases) was never merged to `dev`. Record
text awaits owner review.
Delivery: M40.3 (spawn-controller, #601), M40.4 (harness-runtime, #602),
M40.6 (task-engine, #604).
Supersedes: ADR-0040 (single-stack supervisor). Partly supersedes:
ADR-0090 (lane key and fairness kept; their owner moves to task-engine).
Keeps: ADR-0014 (scale-to-zero), ADR-0036 (run budget, uncertain
reconciliation), ADR-0101..ADR-0104 (backends, behind the M40.15
interface).

## Context

- Container authority is split: the supervisor spawns runtimes, ToolHub's
  workload controller spawns cells, and `hubctl`/`space apply` renders
  Compose. Three writers reconcile overlapping Docker objects by label.
- Three job ledgers (supervisor jobs and lanes, communication spool,
  ToolHub automation tasks) each implement claim, retry and uncertain
  handling. M40.0 (CHG-0333) fixes the live wedges these produce; the
  catalog is [state-machines.md](../state-machines.md).
- The runtime container holds Hermes plus egress, ToolHub and model relays,
  so untrusted execution also hosts boundary code.

## Decisions

1. **spawn-controller is the single container authority.** It owns Space,
   PassportRevision per layer, Runtime, Worker, mounts, image pins and the
   warm pool, in `spawn.db`. Other services request runtimes and workers
   through its contract; none holds Docker or cluster credentials. The
   workload controller merges into it; cells become Workers.
2. **Spawn from prebuilt images.** spawn-controller merges the layer
   passports into the effective spec plus digest, claims a warm runtime for
   the image digest or starts one from the pinned prebuilt image (never
   builds on spawn), and mounts only that space's workspace (narrowed for a
   subagent) and its config slice.
3. **Warm pool.** Keyed by image digest. Tenant and policy digest are bound
   at claim; a revoke or policy change drains runtimes with the old digest.
   No cache or snapshot is shared across tenants.
4. **Runtime lifecycle.** `absent -> starting -> warm <-> busy -> draining
   -> absent`, plus `failed`, `quarantined` and `missing` (inspect finds
   nothing). The lease count is derived from lease rows. A generation
   counter increments on every `starting`; it is the `gen` claim of run
   tokens (ADR-0106 D5). Table: [state-machines.md](../state-machines.md).
5. **task-engine owns the one job ledger.** Job, Run, conversation queue
   and run events in `task.db`. Every job carries `origin owner|unprompted`
   and `class interactive|scheduled|subagent`, and an idempotency key on
   create (channel message id or occurrence id), so repeated wakes
   collapse.
6. **Job lifecycle.** `queued -> admitted -> running -> succeeded | failed`,
   plus `waiting_owner` (holds an OwnerDecision id), `uncertain` and
   `cancelled`. Every non-final state has a deadline and a sweeper and
   names who acts next. `uncertain` leaves only by observation or by the
   owner; it is never retried automatically. Table:
   [state-machines.md](../state-machines.md).
7. **Conversation queue.** `queue` (default) and `interrupt` (`/stop`).
   Panic stop cancels all jobs of a principal. Run events are append-only
   with a cursor for streaming and resume.
8. **harness-runtime is only execution.** Hermes plus the private job
   contract, session slots (N concurrent sessions, default limit 5) and the
   tools-daemon executor for workspace tools. A runtime serves exactly one
   (space, agent, policy digest); narrower rights get a separate runtime.
   Run identity comes from the runtime's own session table, never from the
   request body or headers. Hermes does not load configuration from the
   workspace. Model and egress relays move to model-gateway and
   egress-gateway.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| any service -> backend | only spawn-controller | direct Docker or cluster calls from another service |
| task-engine -> spawn-controller | runtime request for an admitted job | spawn without a job or operator action |
| spawn-controller -> runtime | its own space's workspace and config slice | parent directories holding other spaces |
| runtime -> runtime | nothing | any cross-space path |
| session -> session | nothing | another session's run id |

## Alternatives

- **Keep three container writers with label conventions.** Rejected:
  overlapping reconcilers produced orphan and finalizer incidents
  (ADR-0056).
- **Durable-workflow library for the ledger.** Rejected: own FSM,
  idempotency key and lease cover the need without a dependency.
- **Late bind mounts on Docker for pooling.** Rejected: Docker cannot add
  mounts to a started container; the pool binds tenant and digest at claim
  instead.
- **Browser tab leases and instant-space pods now.** Moved: browser work is
  M42; space creation stays a spawn-controller operation measured by
  SPEC-0101.

## Preserved v0 invariants

- Default-deny: spawn renders only granted capabilities.
- Fail-closed passports: no active revision, no spawn.
- Uncertain outcomes are never retried (D6).
- Credentials never enter the model plane: runtimes receive references.
- Scale-to-zero: idle runtimes stop; homes persist.
