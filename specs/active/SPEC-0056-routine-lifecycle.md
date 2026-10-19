# SPEC-0056: Routine lifecycle and interval windows

Frozen: 2026-10-17. Amendment to SPEC-0013/0048/0054/0055.
Decision: [ADR-0042](../../docs/adr/ADR-0042-routine-lifecycle.md).

## Requirements

- R1.1: Agent tools expose create/list/get/update/pause/resume/snooze/delete/history/
  preview with documented required arguments and the existing exact capability
  checks. No operation can read or mutate another principal/context.
- R1.2: List uses stable ID cursor pages (default 20, maximum 100), optional kind
  and state filters, and compact cards. Get includes complete action, timing,
  next actual due, revision and execution/delivery outcome. Retired records remain
  readable for history but cannot be reactivated. Oversized/invalid HTTP results
  are explicit errors, never successful empty results.
- R1.3: Business mutations require expected_revision; concurrent stale mutations
  return conflict without writes. Update changes only supplied editable fields,
  supports clearing optional settings, and validates the resulting configuration.
  Routing/identity/server outcome fields are rejected. Kind conversion preserves
  ID/history, clears incompatible action fields and validates the new kind.
- R1.4: Text edits preserve next due, snooze and pause. Timing changes clear snooze
  and recalculate due; do not implicitly resume a paused/completed routine. Resume
  advances recurring schedules from now. Expired once schedules require a new due
  time. Snooze affects one next occurrence and retains recurring cadence.
- R2.1: every:<duration> accepts integral seconds from 1 minute through 365 days.
  timing has optional windows [{from,to}], weekdays [0..6], at [HH:MM], start_at
  and end_at RFC3339. At most 8 windows and 24 extra times; invalid/duplicate bounds
  are rejected. Windows apply to interval schedules; other expressions reject
  nonempty timing. Empty timing clears it on update.
- R2.2: Local windows include their opening and exclude their close. Overnight
  windows belong to their opening weekday. Cadence restarts at each window's
  opening. Extra times obey selected weekdays. Global bounds include start and
  exclude end. Preview and ticking share this evaluator. Gaps skip; repeated DST
  slots are distinct instants. No timing change wakes the model to calculate dates.
- R2.3: Preview returns up to 20 upcoming instants with timezone and policy; no
  write, probe or delivery. Exhausted bounded schedules settle completed.
- R3.1: History paginates retained occurrences with revision, due, action kind,
  execution and verified delivery state. It never exposes foreign records even
  after ID collision/retention; retention limits are explicit.
- R3.2: Existing catch-up, occurrence deduplication, dispatch authorization,
  uncertain-send handling and compaction invariants continue to hold. Stream
  subscriptions cannot claim interval-window behavior unless their loop honors it;
  unsupported combinations are rejected.

## Acceptance

- A1: Real filesystem HTTP lifecycle including stale concurrent writes, foreign
  owner, retired reads, paused edits, once-to-recurring conversion and snooze-edit.
- A2: More than 64 KiB of routine inputs remain fully discoverable by summary
  pagination and individual get; malformed/oversized upstream output fails.
- A3: Deterministic interval/window fixtures cover boundaries, overnight weekdays,
  extra slots, end exhaustion, restart, DST and preview/dispatch agreement.
- A4: Managed ToolHub advertised schemas execute read/create/edit/pause/resume/
  snooze/history/delete with actual runtime/catalog versions. Telegram reminder
  acceptance proves delivery separately from tool contract tests.
- A5: just check passes with own Go statement coverage >=85%; relevant image
  checks pass or are explicitly recorded as unavailable, never inferred from mocks.
