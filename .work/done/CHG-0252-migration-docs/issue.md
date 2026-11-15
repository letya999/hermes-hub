## Essence

Retire obsolete editable YAML/JSON authority after a verified, reversible migration and make the new model understandable to operators and agents.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R11, A11).
- Stage plan: `.work/in-progress/CHG-0252-migration-docs/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S11.1 | R11.1 | `cmd/hubctl/main.go` | Implement hubctl dry-run/apply with explicit mapping of scope/agent/workspace/settings/profiles/models/profile.lock/grant records, conflicts, digest, backup and rollback fence; never delete unknown or dynamic state. |
| S11.2 | R11.2 | `docs/spaces.md` | Switch every reader and CLI command to the effective source; only then retire obsolete authority inputs and duplicate generated JSON/YAML; preserve ToolHub/Broker/Communication journals, data, sessions and vault. Add operator and user inventory/diff commands that answer routes/models, services, mounts/tree, core/extended toolsets and grants with provenance and deny reasons; update README, architecture, spaces, operations, RBAC, agent-home, tools, routines, model-relay, Broker docs, templates/examples and all four locale catalogs for user-visible strings. |

## Acceptance criteria

- [ ] A11: Legacy two-user fixture migrates with no data loss, current consumers use only new revision, and all documented commands/trees match reality.
- [ ] An operator can answer for one user which models, services, mounts, toolsets and grants are effective, where each came from and why a requested operation is denied.
- [ ] Existing group and organization runtime scopes are preserved or explicitly reported for separate migration; none is silently retyped as a user.
- [ ] Dry-run then apply on disposable dev copies of two real-shaped spaces; inspect plan, backup, effective inventory and rollback fence.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestLegacySpaceMigrationPlanApplyRollbackAndUnknownFiles` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Corrupt source, symlink, stale digest, mixed old/new config or secret-bearing file causes safe refusal without deleting data.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Dry-run then apply on disposable dev copies of two real-shaped spaces; inspect plan, backup, effective inventory and rollback fence.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/spaces.md; .work/milestone-playbook.md; ADR-0091 migration pattern; #208.

## Out of scope / dependencies

- No broad volume prune, automatic destructive deletion or DB substrate migration.
- Dependencies: M29.3-M29.10. Stage issues: #427, #428, #429, #430, #431, #432, #433, #434. 
