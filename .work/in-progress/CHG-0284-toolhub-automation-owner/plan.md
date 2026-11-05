# CHG-0284: M32 bounded assignments

Current contract: [SPEC-0099](../../../specs/active/SPEC-0099-m32-automation-slices.md).
Delivery decision: ADR-0097. This plan replaces the old stage-wide assignment;
dated evidence stays in state.yaml. No new runtime work is claimed here.

## Read and execute

Read AGENTS.md, README.md, docs/index.md, the assigned GitHub issue and the
contract's invariants plus its row. Historical ADR/spec/audit chains are optional
reference, not prerequisites for every worker.
One issue per assignment/worktree/PR; do not implement this whole CHG at once.

## Assigned slices

- A1 / #521: deliverable, acceptance and exclusions are frozen in the contract row.
- A2 / #522: deliverable, acceptance and exclusions are frozen in the contract row.
- A3 / #523: deliverable, acceptance and exclusions are frozen in the contract row.

#521 after foundation; #522 after #521/#514; #523 after #522.
The old CRUD-first split is retired. Only #523 switches all live ownership.

Code starting points: internal/routines/engine.go; internal/communication/spool.go; internal/communication/assignments.go; internal/communication/occurrences.go; internal/toolhub/server.go.
Follow callers before edits; listed anchors do not limit required caller fixes.

## Verification and boundaries

Each contract row is separately reviewable; record checked/pending acceptance
items, exact SHA, commands/exit codes and remaining risks in this CHG evidence.
Run scoped regressions, just check (own Go coverage >=85%) and applicable
AGENTS.md image/security gates. Mocks are not integration proof.
Tool contract changes ship description/prompt/docs/catalog together; localized
text and safe audit rules apply. Preserve user data; no compatibility fallback,
new service/database/queue or upstream Hermes patch.

Shared-file/merge order and ID dependencies:
[parallel-work](../CHG-0281-m32-per-space-memory-plane/parallel-work.md).
GitHub is the current issue-status authority. Do not mark the whole CHG done
when only one slice passes; no automatic assignment of the next slice.

