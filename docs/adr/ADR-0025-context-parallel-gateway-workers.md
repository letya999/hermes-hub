---
description: Gateway executes jobs with a bounded worker pool; ordering is serialized per context key, never globally.
last_verified: 2026-09-24
---
# ADR-0025: Per-context parallel gateway workers

Accepted 2026-09-24.

## Decision

Amend ADR-0007's "one worker with global concurrency one": the communication
gateway runs a bounded pool of job workers (`HUB_COMMUNICATION_WORKERS`,
default 4). The single file-spool `pending/` queue is preserved — jobs are
claimed in FIFO order, but a candidate whose `(principal_id, context_id)`
already has an in-flight job is skipped, so one context's long-running job
never blocks other contexts.

Ordering inside one context stays strictly serial, matching the supervisor's
per-context execution rule (SPEC-0012). A context's key is held in memory by
the owning job until its mapping reaches a terminal status; an `uncertain`
outcome keeps the context blocked (the run may still be executing) while its
own requeued observation remains claimable. Only an *admitted* run holds the
key: a mapping without `run_id`/`session_id`/`runtime_generation` (e.g. the
supervisor rejected the acquire) has nothing executing and nothing
re-observable, so it releases instead of deadlocking the context. The
in-flight set is rebuilt from such admitted non-terminal mappings on startup,
so the durable spool remains the only state.

## Why

A single global worker made any slow job (multi-minute MCP onboarding,
installations) a head-of-line blocker for every user. The supervisor already
supports concurrent runtimes across contexts; the gateway claim was the only
global serializer.

## Consequences

- Users/contexts no longer wait for each other; per-context FIFO and
  idempotency/durability contracts are unchanged.
- A permanently `uncertain` run still fails closed, but now only for its own
  context instead of the whole gateway — and only when the run was admitted;
  unadmitted failures never block anyone.
- Worker count above the supervisor's `max-concurrent` only queues inside
  `Acquire`; keep defaults aligned.
