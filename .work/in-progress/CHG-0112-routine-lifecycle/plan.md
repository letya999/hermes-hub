# CHG-0112: Complete routine lifecycle and interval windows

Chain: ADR-0042 D1-D7 -> SPEC-0056 R1-R3 -> this plan -> state.yaml.
Evidence: research.md. Previous stage: CHG-0111.

## Goal

The agent can inspect and manage every routine in its authorized context, with
complete lifecycle state, safe edits and predictable interval/window schedules.

## Read before coding

AGENTS.md, README.md, docs/index.md, docs/architecture.md, milestone-playbook.md,
ADR-0041, SPEC-0055, current scheduler/HTTP/agent-tools/projection tests.

## Preconditions and rules

The working branch contains 121 changed paths from related tasks. Preserve them;
do not reset, stash or commit them. Continue on the current branch because the
implementation depends on those uncommitted recovery/runtime changes. Publication
and issue closure are separate from local acceptance. Red tests before changes;
review assertions before running; no new framework or upstream modifications.

## Slices

### S1: Lifecycle and ownership (R1, R3)

Anchors: communication/schedules.go CRUD; http.go routine routes; occurrences.go
history storage; agenttools/routines.go transport.
Add revision-fenced typed patches, get/resume/history/preview and paged summaries.
Tests: snooze/edit, paused text edits, stale concurrent mutation, foreign/retired
reads, kind validation, retained history and bounded responses.

### S2: Intervals and calendar windows (R2)

Anchors: nextExpression/createScheduleAt/settleScheduleLocked; shared evaluator.
Add interval anchor and bounded window/weekday/extra-time options, same preview
and runtime evaluator; tests for cadence, boundary, DST, exhaustion and restart.

### S3: Published tool contract and delivery (R1.1, R3.2)

Anchors: agenttools Input/capability/call/tools_api; stack prompt contract and
managed catalog provisioning. Exact schemas and grants, errors never success.
Rebuild affected dev services, verify images/endpoints, exercise actual managed
tools and authorized Telegram fixture; just check and relevant container gates.

## Red-team checklist

- Foreign principal/context, forged routing/history, stale revision: no mutation.
- Empty/large/malformed list: explicit status and bounded pagination.
- Snooze then timing edit; paused kind conversion; retired ID: consistent state.
- Invalid windows, timezone, durations, DST gap/fold: bounded evaluator.
- Provider uncertain response/restart: no automatic duplicate send.
- Runtime/catalog mismatch: deployment repair, no permissive decoder.

## Acceptance

SPEC-0056 A1-A5. Record exact commands/outcomes in state, separate deterministic
fixtures from managed execution and external delivery. Document unrun gates.

## Out of scope

New scheduler/database, upstream changes, arbitrary cross-context or destination
access, forced replay/backfill, read receipts and killing already admitted work.

## Done checklist

- [ ] Red/green tests, just check and relevant image gates.
- [ ] Red-team, managed lifecycle and real delivery evidence.
- [ ] Current docs, specification and state updated.
- [ ] Move to done only after local acceptance; publish only when instructed.
