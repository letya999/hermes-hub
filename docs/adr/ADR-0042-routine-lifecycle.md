---
description: Owner-scoped routine lifecycle, revision fences and bounded calendar windows through ToolHub.
last_verified: 2026-10-17
---
# ADR-0042: Routine lifecycle and interval windows

Status: accepted direction, 2026-10-17, by the owner's implementation request.
Implementation and acceptance remain tracked in CHG-0112.

Requirements: [SPEC-0056](../../specs/active/SPEC-0056-routine-lifecycle.md).
Evidence and alternatives: [research](../../.work/in-progress/CHG-0112-routine-lifecycle/research.md).

## Context

The scheduler already has durable occurrences, delivery receipts, owner isolation
and recovery. Its agent interface lacks get/resume/history/preview, complete edits
and pagination. A 64 KiB truncated list can report success without a result; an
expression edit retains an old snooze deadline. Deployed runtime/catalog versions
also differ. Adding tool names alone cannot establish working lifecycle control.

## Decisions

- D1: Keep the communication spool as the sole clock and state owner. Publish
  narrow business operations through the existing managed ToolHub executor; no
  second scheduler, database, dependency or upstream Hermes patch.
- D2: Add get, resume, history and preview. List returns bounded summary pages;
  get returns complete configuration. Mutation returns durable state. Identity,
  delivery audience and historical evidence are not editable by a model.
- D3: New business mutations use an expected revision under the spool lock.
  Conflicts require read and deliberate retry, never silent overwrite. Existing
  internal callers remain compatible. Edits preserve pause; resume is explicit.
  Timing edits clear snooze. Changing action kind clears incompatible old action
  settings and validates the complete new action before persistence. Revision
  changes fence queued old work; already delivered messages cannot be recalled.
- D4: Keep once/after/cron compatible. Add anchored every:<Go duration> intervals
  (minimum one minute), optional local HH:MM windows, weekdays, extra local times,
  and absolute start/end bounds. Windows are start-inclusive/end-exclusive and
  may cross midnight. Window cadence starts at its local opening; without windows
  it is anchored at creation. Exact extra times form a union, not another routine.
  UTC is the persisted instant; IANA timezone determines calendar boundaries.
- D5: Preview is read-only and uses the same evaluator as dispatch. It states the
  next actual due time and future slots. DST gaps skip nonexistent local slots;
  repeated local clock times may have two distinct UTC slots. Catch-up stays
  bounded; no burst replay of every interval missed during downtime.
- D6: History is bounded and owner-checked across hot records, archives and
  retained receipts. Report execution and delivery separately; sent is not read,
  and uncertain is not success or an invitation to resend.
- D7: Verify the deployed managed tool path, catalog schema and runtime binary
  together. Strict catalog decoding stays strict; repair version drift through
  the supported rebuild/projection paths, never by accepting unknown fields.

## Alternatives and invariants

Full Temporal/RRULE machinery and a database migration would add operational cost
without solving a demonstrated storage problem. More prompt instructions cannot
repair missing arguments, stale revisions or silent truncation. Unrestricted
cross-context listing would violate the existing principal/context boundary.
An execution window means repeated bounded checks, not a continuously running
model or a promise that an already admitted job is killed at window close.
Manual force-run/backfill and arbitrary destination changes are separate authority
contracts; preview never performs external effects.
