# CHG-0252: M29.11 Migrate legacy config safely and document the six-passport operating model (M29 stage 11)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R11 -> this plan -> `state.yaml`.
Previous stage: CHG-0251. Next stage: CHG-0253. Evidence: state.yaml.

## Goal

Retire obsolete editable YAML/JSON authority after a verified, reversible migration and make the new model understandable to operators and agents.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/spaces.md; .work/milestone-playbook.md; ADR-0091 migration pattern; #208.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S11.1 Contract and failing regression (R11.1)

Anchors: cmd/hubctl/main.go; cmd/hubctl/profile.go; cmd/hubctl/apply.go; internal/stack/profiles.go; internal/stack/provisioning.go; internal/stack/models.go; docs/spaces.md.

Change:
1. Implement hubctl dry-run/apply with explicit mapping of scope/agent/workspace/settings/profiles/models/profile.lock/grant records, conflicts, digest, backup and rollback fence; preserve or explicitly plan group/organization runtime scopes; never delete unknown or dynamic state.

Tests (red first):
- `TestLegacySpaceMigrationPlanApplyRollbackAndUnknownFiles` — Legacy two-user fixture migrates with no data loss, current consumers use only new revision, and all documented commands/trees match reality.

### S11.2 Implementation and consumer migration (R11.2)

Anchors: cmd/hubctl/main.go; cmd/hubctl/profile.go; cmd/hubctl/apply.go; internal/stack/profiles.go; internal/stack/provisioning.go; internal/stack/models.go; docs/spaces.md.

Change:
1. Switch every reader and CLI command to the effective source; only then retire obsolete authority inputs and duplicate generated JSON/YAML; preserve ToolHub/Broker/Communication journals, data, sessions and vault.
2. Add operator and user inventory/diff commands that answer routes/models, services, mounts/tree, core/extended toolsets and grants with provenance and deny reasons; update README, architecture, spaces, operations, RBAC, agent-home, tools, routines, model-relay, Broker docs, templates/examples and all four locale catalogs for user-visible strings.

Tests (red first):
- Extend `TestLegacySpaceMigrationPlanApplyRollbackAndUnknownFiles` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Corrupt source, symlink, stale digest, mixed old/new config or secret-bearing file causes safe refusal without deleting data.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A11: Legacy two-user fixture migrates with no data loss, current consumers use only new revision, and all documented commands/trees match reality.
- Live: Dry-run then apply on disposable dev copies of two real-shaped spaces; inspect plan, backup, effective inventory and rollback fence.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No broad volume prune, automatic destructive deletion or DB substrate migration.
- Dependencies: M29.3-M29.10.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
