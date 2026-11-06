# CHG-0286: M32 bounded assignments

Current contract: [SPEC-0098](../../../specs/active/SPEC-0098-m32-memory-slices.md).
Delivery decision: ADR-0097. This plan replaces the old stage-wide assignment;
dated evidence stays in state.yaml. No new runtime work is claimed here.

## Read and execute

Read AGENTS.md, README.md, docs/index.md, the assigned GitHub issue and the
contract's invariants plus its row. Historical ADR/spec/audit chains are optional
reference, not prerequisites for every worker.
Owner update 2026-11-06: one persistent worktree/branch per track; execute
small stages sequentially, with an issue commit after its coherent contract passes.
Use the current parallel-work plan and CHG-0289 Devin handoff; no new foundation.

## Assigned slices

- M3 / #515: deliverable, acceptance and exclusions are frozen in the contract row.
- M6 / #518: deliverable, acceptance and exclusions are frozen in the contract row.
- M7 / #519: deliverable, acceptance and exclusions are frozen in the contract row.
- M8 / #520: deliverable, acceptance and exclusions are frozen in the contract row.

#515 after #513/#514; #518 after #515/#516; #519 after #518;
#520 after #517/#519. Full memory acceptance is later #526.

Code starting points: internal/agenttools/memory_search.go; internal/agenttools/memory_card.go; internal/toolhub/gateway_call.go; internal/vault; internal/communication/approval.go.
Follow callers before edits; listed anchors do not limit required caller fixes.

## Verification and boundaries

Each contract row is separately reviewable; record checked/pending acceptance
items, exact SHA, commands/exit codes and remaining risks in this CHG evidence.
Run scoped regressions and fast just check; full race/coverage (own Go >=85%)
remain CI gates under CHG-0289. Apply the current
AGENTS.md image/security gates. Mocks are not integration proof.
Tool contract changes ship description/prompt/docs/catalog together; localized
text and safe audit rules apply. Preserve user data; no compatibility fallback,
new service/database/queue or upstream Hermes patch.

Shared-file/merge order and ID dependencies:
[parallel-work](../CHG-0281-m32-per-space-memory-plane/parallel-work.md).
GitHub is the current issue-status authority. Do not mark the whole CHG done
when only one slice passes; continue the owner-authorized track sequence after
recording the completed stage and actual dependency blockers.

