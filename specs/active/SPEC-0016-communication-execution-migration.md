---
status: active
title: Per-context communication execution rollout and rollback
---

# Host selection

1. `spaces/<user>/execution.<env>.json` records schema 1, exact user and dev/prod
   environment, `legacy` or `supervisor` mode, supervisor origin, native cron
   disposition and the compatibility release retaining fallback. It contains no token.
2. The selection is host-owned. Hermes receives the scope root read-only and only
   data directories writable. Model/provider text cannot select execution mode.
3. Absence keeps the previous release's environment-selected behavior. A saved
   selection overrides the global supervisor env, including explicit rollback to legacy.
   A corrupt, wrong-user, wrong-environment or unsupported selection fails closed.
4. Selection does not move the Hermes home, workspace, stateful connections, archive,
   spool, sessions, jobs, receipts or deliveries. Executor routing does not grant tool
   rights or change the effective policy digest.

# Migration contract

1. `hubctl execution-audit --spool <mounted-spool> --user <user>` is read-only.
   Queued jobs require a verified envelope and matching immutable job mapping.
   Claimed jobs and admitted nonterminal runs must settle before switching executors.
   Uncertain external outcomes are retained for inspection and never retried by migration.
2. `hubctl select-execution` defaults to dry-run. Apply requires all selected Compose
   services stopped and the selected supervisor runtime drained/stopped. It checks
   actual private supervisor status; another context may remain running.
3. The operator must provide the actual mounted spool, explicit native cron
   disposition and compatibility release. Apply verifies the stopped selected gateway's
   `/data` mount matches this spool. Concurrent selectors use an exclusive lock.
   Selection is atomically saved before render. Render failure leaves services stopped
   and the selection available for repair; it does not claim execution success.
4. Native cron disposition is an operator declaration, not a guessed automatic import.
   Supervisor apply also uses the pinned upstream SDK in a disposable snapshot of the
   selected home, requiring zero enabled jobs without modifying the source home.
   `unmigrated` refuses scale-to-zero selection and keeps static native Gateway compute
   pinned on in legacy mode. `migrated` means the separate verified schedule-owner
   migration record exists. Selecting an executor never reactivates a native schedule.
   The native `cronjob` toolset is omitted from migrated runtime configurations.
5. Rollback repeats the same stop, drain, audit, select, render and up sequence for
   one context with its existing home and spool. Pending deliveries remain pending;
   sending/failed uncertain deliveries remain uncertain.

# Execution and idle lifecycle

1. The production supervised gateway sends `POST /v1/jobs` and observes admitted runs
   through `/v1/resume`, without input replay. `/v1/execute` remains a compatibility
   alias until the fallback deletion gate passes. Private authenticated
   `GET /v1/jobs/<id>` returns stored identity, status and outcome, never the input.
2. Each configured channel user has its own runtime ID and policy version. A process
   env runtime ID must not redirect another user's tasks or controls.
3. Supervised containers load the same generated provider env and approved features
   as static containers. Existing `/state/hermes`, connection, home, workspace and
   archive paths map to the same exact host directories. Host settings are read-only;
   Docker control is available only to the host supervisor.
4. Streams, jobs, approvals, lifecycle/uncertain holds and explicit pins protect compute.
   Terminal settlement starts the default five-minute idle window. New work cancels
   shutdown and extends that window. The automatic sweep runs at most five seconds
   apart, without expiring unresolved operations by their timestamp alone.
5. Idle shutdown verifies immutable ownership, sends graceful Docker stop, then removes
   only that container. It never deletes volumes or context data. A later job creates
   one replacement generation with the original logical runtime and session mapping.
6. Supervised self-environment overlays reload at the next cold start; the gateway does
   not issue an unscoped restart against a shared supervisor after every final reply.

# Acceptance and deletion

- Real pinned Hermes/Docker evidence supplements unit/race tests.
- The production gateway path proves two verified users with exact homes, duplicate
  update suppression, persistent sessions, warm reuse and released terminal leases.
- A real wall-clock five-minute idle interval proves automatic compute shutdown and
  cold restoration, without manually advancing reaper time for this scenario.
- Migration/rollback, queued-work preservation, claimed/uncertain refusal, ownership
  and concurrent selectors have regression coverage.
- `just check` must pass with own Go statement coverage >=85%; Docker gate must pass.
- Legacy deletion remains blocked until one compatibility release and accepted real
  runtime evidence, as required by issue19 and the migration map. Recording a release
  name in selection is not publication or proof that its acceptance window passed.

Related: SPEC-0012, SPEC-0014, SPEC-0015 and issue19. Full recurring routines,
timezone/DST and native schedule import remain issue33.
