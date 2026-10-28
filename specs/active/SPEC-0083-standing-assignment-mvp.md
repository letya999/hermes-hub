# SPEC-0083: Executable standing-assignment MVP

Frozen: 2026-10-28. Extends SPEC-0080/0081 under ADR-0082 for M23 #324/#332.

## Requirements

- R1: A source with `cursor_arg` receives its last settled `next_cursor` on
  the next read. A silent bootstrap establishes the current high watermark.
  Each source advances independently only after every event in its page was
  dropped by a valid deterministic gate or admitted to its sink. Overflow,
  invalid filter output, unavailable script, missing/expired cursor or sink
  error must leave that source replayable and visible as unhealthy.
- R2: Telegram group monitoring reads message text through a non-consuming
  bounded cursor tool. Pages are oldest first, at most 32 events, with stable
  message IDs; a multi-account read requires an explicit account. The tool
  never acknowledges or removes incoming messages.
- R3: A filtered batch may create at most one specialist job. That job carries
  owner intent, prior progress, untrusted evidence, assignment ID, plan
  generation and trigger job ID. A paused, completed, cancelled or re-planned
  assignment may neither claim that job nor send its pending event delivery.
- R4: The specialist records a bounded summary, finding and next step through
  `assignment_progress` while its trigger job runs. The hub verifies the
  job's owner, assignment and plan generation, and CAS-guards the update.
  Completion retires sources; a retry of the same trigger is idempotent.
- R5: An assignment event source with `sink=agent` must provide a deterministic
  filter. Default grant bounds cannot be exceeded by agent creation. Only an
  explicit owner chat command can authorize larger limits. Global stream
  acquisition has a finite concurrency bound and healthy poll sources have
  a declared interval.
- R6: The immutable agent-tool catalog version, descriptions, prompt contract
  and docs change together. Managed runtimes must rebind and respawn before
  acceptance. Unit tests do not count as live provider or Telegram delivery
  evidence.

## Acceptance

Fixture tests prove source burst replay, filter failure, selective wake,
stale-job cancellation, progress ownership/CAS and completion. The real dev
stack separately proves Telegram read, relevant/irrelevant message filtering,
one wake per batch, progress persistence, restart replay and pause/cancel
fences without external sends until the owner authorizes that live action.
