# CHG-0257: stack.yaml drives the compose project, images and spawn (M30 stage 3)

Chain: ADR-0093 D2, D6 -> SPEC-0092 R3 -> this plan -> `state.yaml`.
Evidence: `../../done/CHG-0254-passport-gap-audit/research.md` Findings 1–2.
Previous stage: CHG-0256. Next stage: CHG-0258.

## Goal

`stack.yaml` is the build-and-run plan of a space: `identity` plus every
service instance with its mode. Per-space instances (including several
`cliproxy-*`) become exactly those containers, shared ones are consumed
from the infra space, supervised ones are spawned by the supervisor. Adding
a line adds a container; removing it removes it.

## Read before coding

1. SPEC-0092 normative `stack.yaml`; ADR-0093 D6.
2. `internal/stack/compose.go` `compose` (`:52`), cliproxy blocks (`:513`, `:662`), `imageBuild` (`:266`); `internal/stack/config.go` `Settings.Infra`, `Cliproxy`, `ExecutionMode`.
3. `platform/catalogs/3/services.yaml` `infra.*.instances`, `depends_on`.
4. `internal/migration/passport.go` `derivedServices`; `cmd/hubctl` build/up.

## Preconditions and rules

- Build only through `hubctl build`/`up` (BuildKit); `just docker-clean` after other builds.
- Existing shared-infra behaviour must keep working for `artem_main`/`artem_two` after migration (stage 8 re-migrates).

## Slices

### S3.1 Compile stack v2 (R3.1)

NOTE (updated by CHG-0255 S1.0): the v2 source vocabulary (identity.*, services.* keyset-narrow) and normative emission already landed in CHG-0255; this slice reduces to validating the checks below and the migrator edge cases.

Anchors: `internal/passport/passport.go` `classes["stack"]`, cross-file checks; `internal/catalog/catalog.go` `Service`.

Change:
1. Source fields: `identity.user|organization` (invariant, from scope), `services.<instance>.mode` (narrow by ceiling).
2. Instance name `<catalog-id>[-<suffix>]` resolves to a catalog service; mode allowed by catalog class: `per-runtime → supervised`, `per-stack → shared|per-space`, `shared|dedicated → shared|per-space`, `per-space → per-space`.
3. Dependencies derived from catalog `depends_on` (satisfied by any instance of the dependency id in any mode); not emitted.
4. Drop `features`, `mode`, `environment` from stack (features → tools/config in stages 4/6; mode is implied by `hermes-runtime: {mode: supervised}`).

Tests (red first):
- `TestStackPassportNormativeShape` (compile fixture → equals normative example)
- `TestStackRejectsUnknownInstanceAndBadMode`
- `TestStackDependencySatisfiedByAnyInstance` (`model-relay` ← `cliproxy-google-1`)

### S3.2 Compose from the stack passport (R3.2)

Anchors: new `internal/stack/compose_plan.go` `ServicePlan(eff passport.Effective) []PlannedService`; `compose.go` `compose`.

Change:
1. `ServicePlan` returns per-space instances with catalog role/image class and network aliases (`cliproxy-google-1` reachable by that name on agent + runtime networks); shared instances add only network attachment; supervised add only the supervisor selection.
2. `compose` iterates the plan instead of `s.Infra`/`s.Cliproxy` branches; per-instance volumes are named by instance (`cliproxy/<instance>/`).
3. Render reads `LoadEffective` for managed spaces (legacy Settings still feed fields not yet migrated — marked pending in the registry).

Tests (red first):
- `TestComposeHasExactlyPerSpaceInstances`
- `TestAddCliproxyInstanceAddsOneContainer` / `TestRemoveInstanceRemovesContainer`
- `TestSharedInstanceRendersNoContainer`

### S3.3 Image build set (R3.2)

Anchors: `cmd/hubctl` build path; `compose.go` `imageBuild`.

Change: `hubctl build --dir … --env …` builds the image classes of the plan's per-space + supervised instances only (core/control/runtime/browser); reconcile `rebuild` (CHG-0255) passes the changed instances.

Tests (red first): `TestBuildTargetsFromStackPlan`.

### S3.4 Supervisor selection (R3.2)

Anchors: `internal/stack/execution.go`; `internal/supervisor/ensure.go`.

Change: `execution.<env>.json` derives from `hermes-runtime: {mode: supervised}`; a managed space without it fails compile (existing rule moves to v2 shape).

Tests (red first): `TestSupervisedRuntimeSelectionFromStack`.

## Red-team checklist

- User layer adds a service outside the ceiling or `mode: per-space` for a shared-only service → compile error.
- Instance name collision with another space's container name → compose project prefix keeps them isolated (test with two spaces).
- No secrets in stack (only names/modes).

## Acceptance

A3 live: add `cliproxy-google-2: {mode: per-space}` → `space apply` shows
`rebuild`, exactly one new container `…-cliproxy-google-2-1` appears and is
healthy; remove it → only that container disappears. Two spaces keep
isolated projects.

## Out of scope

Model routing to the new instance (CHG-0261); config ports (CHG-0258).

## Done checklist

- [ ] Tests red then green; `just check`; `just docker-check`; docker-clean.
- [ ] Live A3 in state.yaml.
- [ ] docs/spaces.md, docs/architecture.md, docs/services/hubctl.md updated.
- [ ] Issue closed; state done; folder moved.
