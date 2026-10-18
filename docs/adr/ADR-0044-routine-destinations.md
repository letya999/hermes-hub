---
description: Bind routine delivery to verified owner tasks using per-run origin metadata.
last_verified: 2026-10-18
---
# ADR-0044: Routine delivery destinations

Status: Accepted. Amend ADR-0042 routing immutability and ADR-0043 tool arguments;
the public surface remains five tools. Requested by the owner.

## Decisions

- D1: create accepts origin_job_id from trusted per-run instructions and optional
  destination_task_id. Missing destination means the exact origin task, never a
  runtime-global current pointer. Explicit destinations are live owned Telegram
  tasks in the authenticated context; the hub derives chat/topic/envelope.
- D2: list view=destinations discovers bounded task summaries; get shows the saved
  named destination. edit action=update can patch destination_task_id; current
  requires origin_job_id. Revision checks and target validation share the writer
  lock. Raw routing and identity remain outside the agent patch contract.
- D3: Rebinding governs future admission. Already admitted work retains its saved
  audience. Trusted prior envelopes preserve authorized historical outcomes across
  an owned-chat change; callers cannot inject that history at creation or edit.
- D4: The private gateway HTTP/operator create contract remains compatible.
  Public catalog 4.0.0 fails explicitly when neither origin nor target is supplied.
  Narrow managed grants are never migrated automatically.
- D5: New job mappings pin the origin-instruction version. Existing mappings keep
  their prior instruction payload, preserving admission replay idempotency.

## Boundaries

Use existing task bindings and spool storage; no database, provider API guess or
upstream patch. Telegram delivery destination and Hermes execution session are
separate. Closed/foreign/stale bindings fail explicitly, without fallback delivery.
Slacks and arbitrary chat destinations are outside this Telegram task contract.

Requirements: SPEC-0058. Implementation/evidence: CHG-0114.
