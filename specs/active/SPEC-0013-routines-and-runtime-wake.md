---
status: active
title: Durable routines and runtime wake-up
---

# Schedule contract

1. The communication control plane is the single durable owner of routine schedules,
   occurrence creation and delivery targets. Hermes may create or edit a schedule
   only through authenticated hub tools after a concrete owner instruction.
2. A schedule records an immutable `schedule_id`, owner principal and context,
   timezone, expression, job kind, bounded input, target conversation/audience,
   enabled state, next occurrence and revision. It contains no credential value.
3. `routine_create`, `routine_list`, `routine_update`, `routine_pause` and
   `routine_delete` validate ownership and current policy outside the model.
4. Each due occurrence becomes an ordinary durable job with `trigger=cron` and an
   idempotency key derived from `(schedule_id, occurrence time, schedule revision)`.
   The job acquires or wakes the same context runtime used by interactive work.
5. Agent routines execute through Hermes. Script-only routines execute through the
   bounded CLI runner when their manifest explicitly permits it; they do not receive
   a general shell.

# Timing and recovery

1. One scheduler owner claims occurrences. Restart or duplicate polling cannot create
   a second occurrence for the same idempotency key.
2. Runs of one schedule do not overlap by default. A still-running occurrence causes
   the next occurrence to be skipped or coalesced according to an explicit schedule
   policy; it is never started implicitly in parallel.
3. Catch-up is bounded. The default is at most one missed occurrence within a
   configured window; older occurrences are recorded as missed, not replayed in a
   burst.
4. Current context, connection and ToolHub policy are re-authorized at execution time.
   Disabled users, contexts, schedules or revoked connectors do not start new work.
5. Delivery uses the durable outbox and the schedule's verified private audience.
   Completion of an external mutation is not reversed by later cancellation or
   revocation.
6. Waiting until a future time is represented as a durable occurrence or continuation,
   not as an idle Hermes process. A currently executing run continues to hold its
   runtime lease until terminal or cancelled.

# Native Hermes cron migration

1. Native Hermes cron and hub schedules must never execute the same routine
   concurrently as dual owners.
2. Existing native definitions are migrated only through a pinned, verified upstream
   export/list contract and an explicit operator apply step. Internal Hermes files are
   not guessed or rewritten.
3. Until a native schedule is migrated or disabled, its context remains explicitly
   pinned on. The supervisor must not scale that context to zero and miss its clock.
4. Migration records source identity, target schedule ID, revision, next occurrence
   and rollback instructions without copying secrets or provider content.

# Acceptance

- Schedules and next occurrences survive hub and runtime restarts.
- A sleeping context wakes for a due occurrence and returns to idle afterward.
- Duplicate scheduler ticks and process restarts create one job and one delivery.
- Paused, deleted, stale-policy and wrong-context schedules fail before runtime start.
- Timezone, DST, overlap and bounded catch-up behavior have deterministic tests.
- Users can list, pause, edit and delete only routines they own.
- A real runtime test demonstrates wake, Hermes execution, durable delivery and sleep.
