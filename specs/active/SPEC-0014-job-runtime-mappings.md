---
status: active
title: Durable job, session, run and runtime mappings
---

# Ownership and records

The communication spool owns a durable mapping for every accepted job. It stores the
job ID, idempotency key and fingerprint, canonical identity and delivery audience,
context/runtime IDs, Hermes session/run IDs, runtime generation, status, timestamps
and the last known event. Prompts and credentials are not copied into mappings.

Mappings use the existing single-host file store with atomic replacement. A
conversation mapping binds one principal/context/conversation to one Hermes session
across warm reuse, restart and cold recreation.

# Idempotency and recovery

An idempotency key can be accepted once. A duplicate with the same fingerprint keeps
the existing job state; a different fingerprint or binding is rejected. Terminal
states are `completed`, `failed`, `cancelled`, and `interrupted`. A lost response
after Hermes admission is `uncertain` and is never silently retried.

Supervisor leases include an owner, expiry and runtime generation. Completion and
release are valid only for the generation that acquired the lease.

Once a mapping has a Hermes session, run or runtime generation, ordinary outcome
updates cannot replace those identifiers. A generation transition requires an
explicit trusted recovery operation, not a late event or response.

An unknown execution response preserves its supervisor lease as `uncertain` until
reconciliation establishes the actual outcome. Disconnect alone is not completion.
Named-lease removal, count adjustment and idle transition are one serialized durable
operation; persistence failure preserves the in-memory hold for a safe retry.

# Event handoff

The persistent execution path supports bounded normalized NDJSON frames. Admission
is recorded before progress, approval or terminal delivery. Each frame has a stable
event identity; a write-ahead receipt owns its mapping position and optional outbox
record. Restart repairs incomplete receipt-to-outbox handoff without another final
delivery. Non-final tool payloads and raw error details are not copied into receipts.

Cached terminal JSON returned by the supervisor resume endpoint follows the same
write-ahead receipt path as a streamed final event. Only explicit resume may rebind
its compute generation; original run, session and complete identity envelope remain
fixed. Telegram results up to 4096 Unicode characters use a text message; longer
results use one response.txt document, preserving the complete result within the
2 MiB output bound. Invalid UTF-8 and oversized output are rejected before sending.

Admitted work recovered from the spool is observed through the authenticated
supervisor resume endpoint. This endpoint resolves the original ownership and run
from durable supervisor metadata, never starts another run, and is the explicit
boundary for rebinding observation to a replacement compute generation.

# Acceptance

- Mapping and conversation state survive communication-hub and supervisor restart.
- Duplicate jobs do not create a second Hermes run or delivery.
- Cross-context and cross-audience records fail closed.
- Unknown external outcomes remain durable for reconciliation.
- State stays in the host control/spool store, outside the ephemeral runtime.

Known admitted uncertain runs may be requeued for observation while Communication Hub remains running. Requeue requires immutable job fingerprint and persisted run/session/generation; unknown admission is never re-executed. Observation retry timing is durable and capped at thirty seconds. The resume request carries no original prompt, and a sensitive input need not be restored to observe its admitted run. Terminal mappings are never requeued.
