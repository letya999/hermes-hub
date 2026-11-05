# CHG-0283: M32 bounded assignments

Current contract: [SPEC-0097](../../../specs/active/SPEC-0097-m32-config-slices.md).
Delivery decision: ADR-0097. This plan replaces the old stage-wide assignment;
dated evidence stays in state.yaml. No new runtime work is claimed here.

## Read and execute

Read AGENTS.md, README.md, docs/index.md, the assigned GitHub issue and the
contract's invariants plus its row. Historical ADR/spec/audit chains are optional
reference, not prerequisites for every worker.
One issue per assignment/worktree/PR; do not implement this whole CHG at once.

## Assigned slices

- C1 / #509: deliverable, acceptance and exclusions are frozen in the contract row.
- C2 / #510: deliverable, acceptance and exclusions are frozen in the contract row.
- C3 / #511: deliverable, acceptance and exclusions are frozen in the contract row.
- C4 / #512: deliverable, acceptance and exclusions are frozen in the contract row.
- C5 / #508: deliverable, acceptance and exclusions are frozen in the contract row.

Config lane order: #509, #510, #511, #512, #508. #508 requires #511/#512.

Code starting points: internal/migration/rights.go; internal/toolhub/api/capability_policy.go; internal/toolhub/capability_store.go; cmd/hubctl/space_apply.go; internal/agenttools/settings.go; internal/stack/hermesconfig.go.
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
