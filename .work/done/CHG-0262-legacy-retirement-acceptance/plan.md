# CHG-0262: Legacy retirement, migration v2, docs and live acceptance (M30 stage 8)

Chain: ADR-0093 D1 -> SPEC-0092 R8, A8 -> this plan -> `state.yaml`.
Previous stage: CHG-0261. Next stage: none (milestone closeout).

## Goal

Managed spaces run only from passports: render refuses without a revision,
legacy authority files are read solely by the migrator, every schema leaf
has a real consumer, existing spaces are re-migrated to the v2 shapes, and
ten spaces built from the normative templates prove every effect class live
with a real model.

## Read before coding

1. All M30 stage plans and their state.yaml receipts.
2. `internal/migration/passport.go`, `audit.go`; `internal/stack/space.go` `ReadEnvironment`.
3. docs listed in S8.3.

## Preconditions and rules

- No silent deletion: legacy files stay on disk, renamed into `generated/migration-backup/<rev>/` only via apply with rollback fence.
- Provider mutations (OAuth logins, real model calls) only on owner instruction.

## Slices

### S8.1 Migration v2 (R8.1)

Anchors: `internal/migration/passport.go` mapping; `space-migrate`, `space-audit`.

Change: map every legacy field into the v2 shapes of stages 2–7 (identity/services+modes, config runtime/ports/channels/supervision/memory, models services/routes, workspace layout/mounts, tools core/extended language from `tools:` entries, grants from section grants + capability ledger); report unmapped fields as conflicts. `space-audit` prints v2 provenance.

Tests (red first): `TestMigrationV2MapsEveryLegacyField` (fixture with every `Settings` field), `TestMigrationV2TwoUserIsolation` (replaces `TestM29TwoUserPassportEndToEnd`).

### S8.2 Retire legacy readers (R8.1)

Anchors: `internal/stack/render_env.go` `RenderEnvironment`; `space.go` `ReadEnvironment`; `consumers.go`.

Change: managed render calls `LoadEffective` only; `ReadEnvironment` callable from `internal/migration` only (package-level guard test); registry `pending:` entries forbidden.

Tests (red first): `TestNoPendingConsumers`, `TestManagedRenderRefusesWithoutRevision`, `TestManagedRenderReadsNoLegacyFile` (fs access recorder).

### S8.3 Documentation (R8.2)

Update: README, docs/index.md, docs/architecture.md, docs/spaces.md (imperative contract, effect classes, apply), docs/rbac.md (rights vocabulary, PDP/PEP), docs/services/toolhub.md (derived records; remove the manual capability sequence added in CHG-0253), docs/services/model-relay.md, docs/services/hubctl.md (`space apply`, `models login`, `grant --cap`), docs/operations.md, docs/agent-home.md, docs/tools.md, docs/routines.md, prompt contract. `devcheck docs` green.

### S8.4 Live acceptance (R8.3, A8)

Procedure (dev stack, branch images via `hubctl up`):
1. Re-migrate `artem_main`, `artem_two` (dry-run, apply, audit); show v2 passports to the owner.
2. Create ten disposable spaces from normative templates with distinct grants/tools/workspace/models; no `hubctl capability` or manual store edits.
3. Per space record: mounts vs `workspace.yaml`; live tool list vs `tools.yaml`; `grants.yaml` vs ledger; real completion via the default route.
4. Effect classes: A2 grant revoke live; A3 add/remove cliproxy instance; A4 port change recreate; A5 writable narrowing live; A6 toolset move live in open session; A7 ladder failover + switch grant (owner-authorized sessions).
5. `just check`, `just docker-check`, `just docker-clean`; receipts without secrets in state.yaml.

## Red-team checklist

- Agent with `config.edit_own` but not `tools.select` cannot widen tools through settings.
- Stale `/config` impossible: compare revision inside container after every apply.
- Cross-user: no space's passport, mount, session or tool visible to another.

## Acceptance

A8 and every stage A-criterion re-run on the final branch build.

## Out of scope

M20/M27/M28 internals.

## Done checklist

- [ ] Tests red then green; `just check`; docker-check; docker-clean.
- [ ] Acceptance matrix in state.yaml; docs updated; indexes synced.
- [ ] Issues closed; states done; folders moved; milestone closed.
