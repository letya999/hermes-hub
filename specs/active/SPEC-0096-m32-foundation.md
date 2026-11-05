---
description: Frozen bounded foundation contract; packaging and verification, no M32 features.
last_verified: 2026-11-05
---
# SPEC-0096: M32 foundation

Frozen: 2026-11-05. Not executed.
Decision: [ADR-0097](../../docs/adr/ADR-0097-m32-bounded-delivery.md).
Scope: one reproducible base before separate implementation worktrees.

## F1. Package

Use the actual source HEAD and an explicit reviewed path allowlist to assemble
existing M32 changes and their necessary prerequisites in
`C:/Users/User/a_projects/hermes-hub-wt/m32-foundation`,
branch `codex/m32-foundation`. Include reviewed new source/docs and deletions.
Do not change source checkout/staging, copy private spaces/credentials/runtime
state, or use bulk add/reset/stash/clean. Record excluded changes.
Commit locally before checks so the verified code has an exact SHA.
If safe separation fails, report the concrete path/hunk; do not guess.

## F2. Verify, with a bounded repair budget

Run `just check` on that candidate; own Go coverage >=85%.
Use AGENTS.md for any applicable image-side Docker gate; the tested image must
match the candidate source. A cached image from another SHA is not evidence.
One localized packaging/fixture correction is allowed, followed by the affected
checks and full gate. A feature gap or wider source failure is a separate repair
assignment: record it and stop foundation expansion. No skipped assertions.
Unavailable checks mean PENDING_GATE, never READY. No user stack/provider changes.

## F3. Handoff

Write `foundation-ready.md` in CHG-0281: source SHA, tested code SHA, included/
excluded paths, actual commands/results/coverage, image identity if applicable,
BASELINE_READY=YES/NO and specific blockers. Separate code SHA from a later
docs-only receipt commit. Existing feature gaps do not become foundation scope.

When ready, publish `codex/m32-foundation` and `codex/m32-integration` without
force, preserving any existing branch work. No dev/main merge.
No implementation issues, additional ADR/spec design, interface matrix,
20-issue audit or lane-readiness report. No implementation workers/worktrees.

## Acceptance / outside scope

A fresh checkout of the published integration SHA contains the selected work,
passes required gates and has a receipt. Source dirty state remains intact.
Foundation completion means a usable development base, not a working M32 MVP.
The first subsequent assignments are #509, #513 and #521; foundation need not
read their bodies. No production deployment, data migration or legacy fallback.
