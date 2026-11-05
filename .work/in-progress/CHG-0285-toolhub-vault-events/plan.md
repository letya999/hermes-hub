# CHG-0285: M32 bounded assignments

Current contract: [SPEC-0098](../../../specs/active/SPEC-0098-m32-memory-slices.md).
Delivery decision: ADR-0097. This plan replaces the old stage-wide assignment;
dated evidence stays in state.yaml. No new runtime work is claimed here.

## Read and execute

Read AGENTS.md, README.md, docs/index.md, the assigned GitHub issue and the
contract's invariants plus its row. Historical ADR/spec/audit chains are optional
reference, not prerequisites for every worker.
One issue per assignment/worktree/PR; do not implement this whole CHG at once.

## Assigned slices

- M1 / #513: deliverable, acceptance and exclusions are frozen in the contract row.
- M2 / #514: deliverable, acceptance and exclusions are frozen in the contract row.
- M4 / #516: deliverable, acceptance and exclusions are frozen in the contract row.
- M5 / #517: deliverable, acceptance and exclusions are frozen in the contract row.

#513/#514 after foundation; #516 after #513/#514; #517 after #515/#516.
This lane does not wait for CHG-0284 as a whole.

Code starting points: internal/toolhub/server.go; internal/stack/compose.go; internal/communication/memory_capture.go; internal/communication/core.go.
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

