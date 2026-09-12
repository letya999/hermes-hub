---
status: active
title: Scale-to-zero Hermes runtime lifecycle
---

# Runtime ownership and persistence

1. A logical runtime is bound by the trusted control plane to
   `(principal_id, context_id, runtime_mode)`. Its `runtime_id` survives process and
   container recreation. Container IDs, PIDs and network addresses are generations,
   not identities.
2. Hermes home, sessions, memory, skills, hooks, plugins, workspace, artifacts,
   connection state and browser profile persist in the owning context home. Stopping
   compute never deletes or transfers them.
3. A runtime mounts only the resolved context paths. The supervisor must not mount the
   complete `spaces/` root into Hermes, and the communication hub must not receive
   scope homes or provider credentials.
4. Personal and organization contexts of the same principal are separate runtime
   bindings and homes.

# Lifecycle

1. The lifecycle is `stopped -> starting -> ready -> busy -> idle -> stopping ->
   stopped`, with an observable `degraded` outcome for failed reconciliation.
2. A durable accepted job, due routine, explicit operator start or resumable approval
   may acquire a runtime lease. Registration alone does not start a runtime.
3. Starts are deduplicated and serialized per context. Different contexts may execute
   concurrently up to a configured global limit; excess work remains queued.
4. The initial implementation serializes jobs within one context. Later concurrency
   must prove Hermes session and filesystem safety before changing this rule.
5. After the final lease is released, the runtime remains warm for a configurable
   duration, default five minutes. New work atomically cancels the pending stop and
   extends the deadline.
6. Idle shutdown is forbidden while a context has queued or running work, an active
   event stream, a pending approval, a lifecycle mutation or an uncertain external
   result. Unknown state fails closed and preserves the runtime for reconciliation.
7. Reconciliation restarts missing runtimes only for current leases or runnable work.
   It detects stale generations and bounded crash loops without making every user
   runtime permanently resident.
8. Each runtime has finite CPU, memory, PID, execution-time, output and restart
   limits. The warm TTL and global concurrency limit are host configuration, never
   prompt or tool arguments.

# Execution contract

1. The host-side Go supervisor is the only component that controls Docker. It exposes
   a private authenticated API to the communication hub and never exposes Docker
   authority to Hermes or channel adapters.
2. The supervisor validates the canonical identity envelope and current runtime
   binding before resolving mounts, environment or credentials.
3. A ready runtime uses the pinned upstream Hermes `/api/sessions` and `/v1/runs`
   APIs. The runtime adapter creates a deterministic context/conversation session,
   submits an idempotent run and consumes bounded normalized events. Terminal
   observation recovers only that original run, never a previous session answer.
   The durable hub and supervisor mappings record
   job, idempotency, context, runtime, Hermes session, Hermes run, status, timestamps
   and runtime generation.
4. Conversation-to-session mapping is stable across warm reuse and cold recreation.
   Duplicate job submission returns the prior known outcome; uncertain outcomes are
   not automatically repeated.
5. Cancellation and approval are bound to the original principal, context,
   conversation and run. Both hold a lease until they reach a terminal state.
6. Runtime and Hermes API ports remain on private or loopback networks. Readiness
   proves the authenticated Hermes API is usable; liveness proves only the owning
   process is alive.
7. The supervisor exposes authenticated lease operations. Lease kinds are `job`,
   `stream`, `approval`, `lifecycle` and `uncertain`; only the opaque lease ID may
   release a lease. Unknown or duplicate releases fail closed.

# Deployment and rollback

1. Docker is the initial single-host backend. A host daemon or service runs the Go
   supervisor; neither the communication nor Hermes container mounts the Docker
   socket.
2. Migration is enabled per context. The current one-shot `hermes -z` path remains a
   release-scoped rollback until real restart, isolation, cancellation, idempotency
   and data-preservation checks pass.
3. Runtime removal is distinct from context-data deletion. Automated lifecycle code
   may stop and remove compute but may never purge a home, credentials, queued
   delivery or schedule.
4. Firecracker/Uno, Kubernetes, PostgreSQL, Redis and multi-host orchestration remain
   out of scope until capacity or hostile-multitenancy evidence requires them.

# Acceptance

- Two concurrent starts for one context create one runtime generation.
- Two contexts never share writable mounts, environment, sessions or credentials.
- A new job during the idle window prevents shutdown.
- A running job or pending approval survives beyond the idle TTL without being reaped.
- An idle runtime stops after the configured TTL and later cold-starts with the same
  Hermes sessions, memory, skills, workspace and logical runtime ID.
- A stopped idle runtime is not recreated by reconciliation.
- Cross-context, stale-policy and prompt-supplied authority fields fail before any
  path, credential or Docker operation.
- Real pinned-Hermes and Docker probes supplement unit tests; mocks alone do not prove
  lifecycle compatibility.

This specification supersedes SPEC-0007 requirement 6 and its always-on/per-user
container deferral, plus SPEC-0009 Hermes-resolution requirement 6 and the static
per-user runtime placement. Other requirements remain active.

# Reconciliation implementation

- Sweeps inspect owner-filtered Docker process state separately from desired jobs,
  holds and pins. Authenticated Hermes readiness and native platform connection
  health are separate observations; lazy external connectors are `not_probed`.
- Authoritative absence permits an idle registration to become stopped after TTL
  and durable state handoff, without starting compute. Missing current work is
  replaced under a per-context recovery coordinator and observed by original run.
  Lifecycle holds retain opaque release IDs across replacement; older ledgers
  without a saved binding remain available for explicit inspection.
- Restored in-flight generations are verified and adopted when running. Lost
  Docker start acknowledgements retain capacity until absence or verified cleanup
  is known. Restored non-stopped entries reserve global capacity.
- Each failed generation is counted once. Recovery waits 2/4/8 seconds and allows
  at most three failures in a ten-minute window; counts and deadlines persist.
  Docker operations have a thirty-second ceiling and a background sweep has a
  150-second ceiling; no new mutations begin after the sweep deadline.
- Ownership requires matching owner/context/generation labels and an actual
  `/scope` bind from the selected context directory, checked by immutable ID.
  Copied labels cannot authorize another context mount.
- Inventory is bounded to 256 owner-labelled containers. Container name, context
  and generation identify current compute. Old generations with a known terminal
  ledger may be removed by verified immutable ID. Unknown or uncertain generations
  remain visible and block additional cold-start capacity. Removal never passes a
  volume-purge option or deletes context paths.
- Pins persist independently of generations and prevent idle shutdown. Pin removal
  neither cancels jobs nor releases their holds. Due routines enter the ordinary
  durable authorized job queue and sleep after final handoff. Unmigrated native
  cron requires an explicit pin; full routine CRUD/timezone migration remains
  SPEC-0013 work.

Pinned Hermes keeps a durable session-turn lease for up to 300 seconds after an
unclean exit when Docker reuses the former gateway PID. A new task in that same
conversation can therefore remain admitted while waiting for the native lease.
Request timeout keeps the task uncertain and held; recovery observes the same
run until it settles. The hub never deletes native locks, sessions or history to
shorten that wait, and never submits a second copy of the input.
