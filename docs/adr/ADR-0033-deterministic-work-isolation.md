---
description: Accepted local spool ownership, durable deterministic result handoff and isolated Starlark execution.
last_verified: 2026-10-11
---
# ADR-0033: Fence deterministic work and isolate script allocations

Status: accepted, 2026-10-11, as part of the owner-approved M10 remediation plan.

## Decision

Keep the local file spool, existing OS locks and supervisor. A gateway owns the
spool throughout its lifetime; offline editors own the same lock until completion.
Do not introduce a database or queue. Canonical result receipts precede outbox
handoff; replay completes the handoff with stable delivery IDs. Schedule revisions
and deletion tombstones fence old work, including queued delivery.

Starlark step limits do not bound allocation or a single native builtin. Execute
untrusted scripts in a disposable core-image container, with no mounts, network
or secrets and explicit memory/CPU/PID limits. A helper wall watchdog bounds a
long builtin. Reuse the supervisor's bounded admission and existing Go helper;
never fall back to executing arbitrary scripts in the gateway process.

## Consequences

Single-host locking is intentional; multiple hosts sharing a filesystem are not
supported. Linux parent-directory fsync strengthens crash durability; Windows has
only content sync before rename. Receipts remain until an explicit retention
policy is selected: automatic removal could destroy deduplication and audit data.
Cancellation fences pending effects but cannot reverse an accepted provider call.
Unsupervised development can test language semantics but cannot execute routine
scripts without the isolated supervisor. Public probes deliberately have no
credentials; private/live knowledge connectors need separate reviewed contracts.
