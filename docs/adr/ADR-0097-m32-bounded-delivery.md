---
description: M32 delivery boundaries and a single automation ownership switch.
last_verified: 2026-11-05
---
# ADR-0097: Bounded M32 delivery

Status: accepted — delivered M32, verified 2026-11-15.
Refines delivery of ADR-0095/0096; their security and ownership decisions remain.
User request: make foundation smaller and give each worker a bounded assignment.

## Observed constraints

- `communication/spool.go` constructs `routines.NewEngine(root, &s.mu, Deps)`
  with locked enqueue/mapping/storage callbacks. CRUD and clock cannot safely
  change owners independently.
- `agenttools.SettingsSet` still writes self-settings; publication/recovery are
  separate concerns. A settings tool must reuse a completed publisher path.
- Selected path scopes already exist in `CapabilitySelection.PathScopes`,
  `NarrowSelectionScopes` and `migration.selectedHubTools`. Their presence does
  not prove the whole authored scope contract or live execution.
- ToolHub startup has no vault-binding reader in `EndpointConfigFromEnv`.
  Backend plumbing can be built without exposing model memory calls.

## Decisions

1. Foundation packages and verifies existing code at one reproducible SHA.
   It implements no feature issue and designs no new cross-service interfaces.
   A failing source gate becomes a named repair task; it does not absorb M32.
2. The first wave is #509, #513 and #521, one issue per worktree.
   #508 waits for #511/#512 so settings edits reuse coherent activation/recovery.
   Shared-file edits are integrated serially; no worker copies a whole peer file.
3. Automation has three reviewable steps: #521 removes shared-lock/callback
   coupling while the existing owner continues to run; #522 builds/tests the
   complete ToolHub host without starting it in production; #523 switches CRUD,
   assignments, watches, state, clock and dispatch together and removes old wiring.
   An inactive constructor is preparation, not a second scheduler or compatibility
   mode. No dual writes, state synchronization, rollout flag or fallback owner.
4. Existing automation records retain their format and ownership is reassigned
   through deployment wiring after the old owner stops. No data conversion or
   automatic migration is introduced; unsupported layouts refuse activation and
   preserve data. Target-specific proof is required before a real switch.
5. Each issue has a current small contract and independent acceptance evidence.
   A backend-only issue can be complete without claiming the product works.
   Real two-user/fault acceptance remains #526; final cutover gates remain #527.

## Current contracts

- [SPEC-0096](../../specs/active/SPEC-0096-m32-foundation.md): foundation only.
- [SPEC-0097](../../specs/active/SPEC-0097-m32-config-slices.md): #508–#512.
- [SPEC-0098](../../specs/active/SPEC-0098-m32-memory-slices.md): #513–#520.
- [SPEC-0099](../../specs/active/SPEC-0099-m32-automation-slices.md): #521–#525.

Accepted historical ADRs and frozen specs are preserved. These contracts restate
the needed invariants so reading all historical records is not a worker prerequisite.
The issue map and CHG plans carry order/status, not new runtime configuration.

## Delivery amendment — 2026-11-05

The owner removed the separate foundation stage after the first wave proved
independent. D1 and the foundation prerequisite in D2 are superseded as delivery
instructions. Review and commit the existing changes, verify that commit, merge
it into `dev`, then start #509/#513/#521 from the same `dev` SHA in three
reusable lane worktrees. One issue still gets one branch and one reviewable PR.
The automation ownership boundaries in D3–D5 remain in force.
