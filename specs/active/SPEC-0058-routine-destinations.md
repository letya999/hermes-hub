# SPEC-0058: Routine delivery destinations

Frozen: 2026-10-18. Amends SPEC-0056 routing edits and SPEC-0057 arguments only.

- R1: Keep five routine tools. create accepts origin_job_id and
  destination_task_id; at least one is required in the public adapter. Default
  target is the source job's persisted task/topic, independent of /use or other
  simultaneous conversations. Gateway legacy HTTP callers retain old behavior.
- R2: Source mapping must match owner, context, runtime, policy and verified
  Telegram peer. Target must be live, in the same owner/context, with a verified
  chat and valid topic binding. Explicit task IDs are resolved by the hub; no raw
  caller-controlled destination chat/topic is trusted on this path.
- R3: routine_list view=destinations returns ID/name/chat/topic/conversation/state
  summaries with query and ID cursor, bounded 1..100. routine_get exposes saved
  destination with its current availability; no silent substitution on archival.
- R4: edit update patch accepts destination_task_id; current requires
  origin_job_id. Validate destination and expected_revision under one spool lock.
  Preserve ID, pause, timing, snooze and history. Old admitted deliveries retain
  their audience; stale stream results cannot route into the new revision.
- R5: Prior envelopes are server-owned metadata for retained history. Creation
  clears caller-supplied history; edits reject it. Keep existing foreign-history
  isolation and verify each delivery against the original occurrence audience.
- R6: Inject origin_job_id only into per-run instructions for versioned new job
  mappings. Never use a mutable process environment or global current-session
  pointer. Legacy mapping replay retains its original instructions.

Acceptance: source/explicit target creation, concurrent source sessions, owned
destination discovery, atomic rebind and stale conflict, archived/foreign/forged
route rejection, history across rebind, HTTP Bot API topic routing, five-tool
published schema, full just check >=85 percent and final dev MCP acceptance.
