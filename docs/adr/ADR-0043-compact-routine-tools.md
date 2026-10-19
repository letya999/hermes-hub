---
description: Consolidate the public routine interface into five task-oriented tools.
last_verified: 2026-10-18
---
# ADR-0043: Compact routine tools

Status: Accepted. Refines the public tool surface of ADR-0042; scheduling and
ownership decisions remain unchanged. Explicit owner direction: five tools.

## Decisions

- D1: Publish routine_list, routine_get, routine_create, routine_edit and
  routine_delete. Server lifecycle operations remain reusable implementation.
- D2: edit requires action=update|pause|resume|snooze and expected_revision.
  update takes patch; snooze takes until. Reject conflicting action arguments.
  One reviewed routines.update grant authorizes edit; deletion retains its own
  capability. Never automatically migrate a narrower old managed grant.
- D3: get optionally includes history and next_runs, with bounded independent
  limits and a history cursor. Default card stays compact. These reads are not
  an atomic cross-endpoint snapshot; failures remain explicit.
- D4: list supports owner-scoped text search before stable pagination. Do not
  replace structured execution/delivery history with unrestricted log access.
- D5: Publish a new immutable catalog version; old definitions remain immutable.
  Retain old private executor operations for already pinned callers, but remove
  their public MCP advertisements from the current catalog.

## Alternatives

Ten wrappers unnecessarily expose API structure. A single action tool merges
reads, creation and deletion into a difficult authority contract. Five tools
separate discovery, inspection, creation, modification and irreversible retirement.
No claim of measured LLM superiority: compare task success in future evaluations.

Implementation: CHG-0113. Requirements: SPEC-0057.
