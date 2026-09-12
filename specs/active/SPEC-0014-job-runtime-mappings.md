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

# Acceptance

- Mapping and conversation state survive communication-hub and supervisor restart.
- Duplicate jobs do not create a second Hermes run or delivery.
- Cross-context and cross-audience records fail closed.
- Unknown external outcomes remain durable for reconciliation.
- State stays in the host control/spool store, outside the ephemeral runtime.
