# CHG-0314: Rights derivation inside the ToolHub writer

Milestone: M9 — Enterprise Readiness (post-IAM hardening)
Trigger: live incident during CHG-0313 apply recovery — host-side
`DeriveSpaceRights`/`RestoreSpaceRights` raced the live ToolHub's own
commits on `store.db`; `saveLocked` digest arbitration returned
`ErrConflict` deterministically, wedging recovery until the container was
stopped by hand.
Branch: feat/m9-rights-derive-in-writer (from origin/dev)
Related: CHG-0301 (read-side: `data_version` blind to foreign commits —
fixed via rollback-journal mode). This change closes the write side:
only the ToolHub process mutates the store, period.

## Problem

`space apply` (and its recovery path) executes rights derivation and
restore by opening `store.db` on the host and doing Load→mutate→save.
The live ToolHub service holds the same file and commits on every tick
(cursor/event/state writes continue even while fenced by a pending
pointer). `saveLocked` compares a file digest captured at Load and
refuses to write over foreign commits — correct single-writer CAS, but
it means the host-side path loses the race against an always-writing
service, deterministically under tick cadence.

Today the only safe procedure is `docker stop toolhub` → apply → restart,
which is undocumented, easy to forget, and leaves a window.

## Goal

All mutations of `store.db` go through the ToolHub process under its own
write lock. The host-side apply becomes a client that asks ToolHub to
derive/restore, never a second writer.

## Design

- **Control endpoint on ToolHub**: `POST /v1/admin/rights/derive` and
  `POST /v1/admin/rights/restore` (auth: enrolled service bearer, same as
  other control routes). Derive accepts the new passport revision +
  effective rights payload; restore accepts the prior revision. Both run
  inside `Store`'s mutex — serialized with tick writes by construction.
- **Shared logic**: `DeriveSpaceRights`/`RestoreSpaceRights` in
  `internal/migration/rights.go` keep their pure computation (profile
  selection/derivation/restore diff) but split into: compute phase
  (pure, testable) + apply phase (store mutation). ToolHub endpoint calls
  compute+apply under one lock; host calls the endpoint.
- **Host path**: `space apply`/`space_apply_recovery` POST to the ToolHub
  control URL (in-network `http://toolhub:…` or operator override — same
  pattern as `HUB_RUNTIME_SUPERVISOR_URL`, e.g.
  `HUB_TOOLHUB_CONTROL_URL`). Timeout + bounded retry on connect errors;
  on non-2xx the apply aborts to the existing fenced-pending flow.
- **Offline mode**: when ToolHub is not running (first bootstrap, dead
  container) the host falls back to direct derive — safe because there is
  no live writer. Detected by control-URL reachability, not by trying
  to write first.
- **Fencing preserved**: while the passport pointer is `pending`, the
  derive/restore endpoints still execute (they are how recovery un-wedges
  the space); they are admin routes, not subject to the runtime fence.

## Non-goals

- No change to the rights model itself (capability profiles, revisions,
  restore semantics) — only who executes the write.
- No distributed locking, no leases, no stop-the-service orchestration —
  the single-writer design makes them unnecessary.
- No change to CHG-0301's rollback-journal mode; the two fixes are
  complementary (read visibility vs write ownership).

## Slices

- S1: split `DeriveSpaceRights`/`RestoreSpaceRights` into compute + apply;
  pure-compute unit tests unchanged-behaviour.
- S2: ToolHub control endpoints + auth + tests (derive, restore, conflict
  ordering with a synthetic tick writer).
- S3: hubctl host path — call endpoints with URL resolution, retry,
  offline fallback, fenced-pending semantics preserved; regression test
  with a stub ToolHub.
- S4: live verification — `space apply` on artem_main dev with ToolHub
  running: no `docker stop`, no ErrConflict, green revision publish;
  forced-failure recovery path likewise.
- S5: docs — operations.md apply/recovery section updated (no manual
  toolhub stop), state.yaml evidence.

## Risks

- Endpoint must not deadlock against the store lock held by a long tick —
  derivation is bounded work; tick cadence is short. Watch: if a tick
  holds the lock long, endpoint latency grows; acceptable for apply-time
  admin ops.
- `HUB_TOOLHUB_CONTROL_URL` naming must match existing override
  conventions and be documented.
