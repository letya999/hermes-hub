# CHG-0244: M29.3 Separate service topology from settings and provision from the effective revision (M29 stage 3)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R3 -> this plan -> `state.yaml`.
Previous stage: CHG-0243. Next stage: CHG-0245. Evidence: state.yaml.

## Goal

Give stack.yaml and config.yaml one responsibility each and render Compose/supervision from their validated pair.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/architecture.md; docs/spaces.md; docs/operations.md; M28 #402/#405.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S3.1 Contract and failing regression (R3.1)

Anchors: internal/stack/config.go Settings; internal/stack/compose.go Compose; internal/stack/render.go Config; internal/stack/render_env.go RenderEnvironment; internal/supervisor/ensure.go.

Change:
1. Move service identity/instances/modes to stack and channel/memory/supervision/port/runtime choices to config, including vault/external memory, nightly rollup, dreams and protected critical stack watchers; keep secret bytes in existing secure sources.

Tests (red first):
- `TestStackConfigRejectsMissingServiceAndPortCollision` — A user can inspect which services run and how they are configured; missing service or conflicting port fails before any compose change.

### S3.2 Implementation and consumer migration (R3.2)

Anchors: internal/stack/config.go Settings; internal/stack/compose.go Compose; internal/stack/render.go Config; internal/stack/render_env.go RenderEnvironment; internal/supervisor/ensure.go.

Change:
1. Validate service dependencies, proxy references, channel voice dependencies, port collisions, managed-mode constraints and service catalog pins before generation.
2. Render compose, service env and supervisor selection from one effective revision; preserve dev/prod isolation and existing shared infra behavior.

Tests (red first):
- Extend `TestStackConfigRejectsMissingServiceAndPortCollision` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- A user config attempts to enable a forbidden service or expose a private port; ceiling rejects it.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A3: A user can inspect which services run and how they are configured; missing service or conflicting port fails before any compose change.
- Live: Render dev stack for shared infra and a secondary user, inspect compose and endpoint health without leaking env values.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No M28 shared edge redesign and no arbitrary container creation.
- Dependencies: M29.2.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
