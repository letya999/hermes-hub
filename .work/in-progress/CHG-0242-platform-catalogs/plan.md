# CHG-0242: M29.1 Generate immutable platform catalogs for capabilities, toolsets, and services (M29 stage 1)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R1 -> this plan -> `state.yaml`.
Previous stage: CHG-0241. Next stage: CHG-0243. Evidence: state.yaml.

## Goal

Replace scattered name/effect/operation lists with one typed release registry and generated immutable catalogs.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and internal/agenttools/capability.go; docs/tools.md; ADR-0089; M27 #383.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S1.1 Contract and failing regression (R1.1)

Anchors: internal/agenttools/capability.go HubToolsDefinition; internal/toolhub/api/governance_io.go SeedInventory; internal/stack/config.go Features; internal/stack/compose.go Compose; internal/toolhub/api/capability_policy.go.

Change:
1. Define typed catalog records for capability operations/scope/issuers, toolsets with members/effects/via/scope support, and service kinds/dependencies; reuse current definitions and preserve immutable hub-agent-tools versions.

Tests (red first):
- `TestPlatformCatalogParityAndPinnedToolset` — Generated catalogs match real handlers and docs, have stable digests, and reject unknown operations/versions without expanding a pinned space.

### S1.2 Implementation and consumer migration (R1.2)

Anchors: internal/agenttools/capability.go HubToolsDefinition; internal/toolhub/api/governance_io.go SeedInventory; internal/stack/config.go Features; internal/stack/compose.go Compose; internal/toolhub/api/capability_policy.go.

Change:
1. Generate platform/catalogs/<release>/{capabilities,toolsets,services}.yaml deterministically; validate handlers, prompt contract, docs/tools.md and docs/routines.md parity.
2. Reject unknown names and unsupported executor parameters, and pin catalog versions in every effective passport; add version bump/rebind checks.

Tests (red first):
- Extend `TestPlatformCatalogParityAndPinnedToolset` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- A new handler is added without catalog/docs/version bump; CI fails instead of exposing it.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A1: Generated catalogs match real handlers and docs, have stable digests, and reject unknown operations/versions without expanding a pinned space.
- Live: Inspect operator catalog and one admitted user catalog after tool bind; only allowed entries appear to the user.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No new arbitrary plugin runtime or pack acquisition; M27 owns packs.
- Dependencies: M29.0.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
