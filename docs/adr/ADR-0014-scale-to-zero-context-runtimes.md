---
description: Start one warm isolated Hermes runtime per active context and stop it after bounded idle time.
last_verified: 2026-09-12
---
# ADR-0014: Scale-to-zero Hermes runtimes per context

Accepted 2026-09-12.

## Decision

Keep durable Hermes state in the owning `spaces/<context-id>` home, but make the
compute runtime ephemeral. The communication hub persists and routes work; a trusted
host-side Go supervisor starts at most one pinned Hermes runtime for an active
`(principal_id, context_id, runtime mode)` binding, reuses it during a configurable
warm window, and stops it after five minutes of inactivity by default.

Idle time starts only after the context has no queued or running job, active event
stream, pending approval or lifecycle operation. New work cancels the pending stop.
Starts and execution are serialized per context while different contexts may run in
parallel under a global limit. Automatic recovery reconciles only runtimes with live
work or leases; it does not recreate every registered user's runtime.

The supervisor is the only component allowed to control Docker. It runs outside the
communication and Hermes containers, derives container names and mounts from trusted
bindings, mounts only the selected context home, injects only that context's runtime
configuration, and exposes no runtime port publicly. Neither `communication-hub` nor
Hermes receives the Docker socket or a root containing other users' homes.

The upstream Hermes HTTP session/run API remains the execution contract. The runtime
adapter creates a stable context/conversation session and submits an idempotent run;
the host supervisor holds typed leases for jobs, streams, approvals, lifecycle work
and uncertain outcomes. Stopping or recreating compute does not change `runtime_id`,
Hermes session ownership or stored memory. A container ID is an expendable runtime
generation, not an identity.

The hub owns durable routine schedules and their delivery targets. At each occurrence
it creates an ordinary idempotent job and wakes the owning context; Hermes executes the
job. This avoids a second clock owner inside a runtime that may be stopped. Native
Hermes cron definitions require an explicit, verified migration; until migrated, a
context with active native cron remains pinned on rather than being silently stopped.

## Consequences

Two hundred registered users produce two hundred isolated homes but only as many live
Hermes runtimes as there are active contexts, bounded by capacity. Cold starts add
latency, so the warm TTL remains configurable and is tuned from measured startup and
resource data. Long waits are represented as durable future jobs instead of keeping a
process alive; an actively executing run continues to hold its lease.

This decision supersedes ADR-0007's fresh-process-per-job placement, ADR-0012's
long-lived-compute assumption, and ADR-0009 only where it describes a statically
deployed per-user runtime. Their identity, API, filesystem and service-boundary
decisions remain in force. Docker is the initial backend; Firecracker, Uno,
Kubernetes, PostgreSQL and Redis remain conditional on measured need.

## Related records

SPEC-0012, SPEC-0013, ADR-0007, ADR-0009, ADR-0011, ADR-0012 and issues #14-#19,
#33, #46 and #55.
