# CHG-0288: M32 integrated acceptance

Current contracts: SPEC-0097, SPEC-0098, SPEC-0099.
This plan replaces the old stage-wide assignment. Prior audits are dated evidence.
Root AGENTS/README/docs index remain required. No new implementation is claimed.

## #526 — fault and isolation acceptance

Depends on #508–#525 integrated into one SHA. Test the actual managed lifecycle,
not hand-built profile fixtures alone. Use two synthetic users and real containers:
- six authored inputs, no effective legacy/self-settings override;
- tools.configure/use independent; selected memory tools inert when disabled;
- two concurrent private/group runs, exact-run denial and no cross-user access;
- durable capture under lost acknowledgment/restart and bounded per-user work;
- remember and confirmed exact edit, stale base/replay/revoke before commit;
- routine/assignment/watch CRUD, current revoke/pause fences, one dispatch after
  competing claims/restart, execution with gateway down and later delivery;
- per-user local-day dreaming, recovery/revoke before commit and digest drafts.

Record source/image identity, commands, effects and boundaries. External provider
delivery exactly once is not inferred from hub idempotency. Failed criteria remain
open under their implementation issues. No provider login/sends without authority.

## #527 — final cutover and projection

Depends on #526. Inventory and verify absence of obsolete config readers/writers,
shared run-scope, runtime/OpenKnowledge vault access and communication-owned clock.
Verify immutable catalog descriptions, prompt_contract, tools/routines docs, locale
parity and actual synthetic managed rebind/respawn. Fix omissions; this is not
permission to postpone per-issue tool contract bundles until the end.
Run just check, own Go coverage >=85%, applicable image gates and security for
dependency changes. Report unavailable/live boundaries honestly.
Update current docs and CHG evidence; move completed CHGs to done with index rows.
No deploy, data migration, fallback, broad cleanup or automatic dev/main merge.

## Traceability

SPEC-0095 R1/R2/R3 and A1–A5 → SPEC-0097 C1–C5 + these two issues.
SPEC-0094 R1/R2 and A1/A2 → SPEC-0098 M1–M8 + #526.
SPEC-0094 R3/R4 and A3/A4 → SPEC-0099 A1–A5 + #526.
SPEC-0094 A5 → required per-issue gates and #526/#527.
Retention, citation and routine behavior invariants remain in the current contracts.
The granular split does not waive any original acceptance requirement.

