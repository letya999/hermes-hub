## Essence

Replace scattered name/effect/operation lists with one typed release registry and generated immutable catalogs.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R1, A1).
- Stage plan: `.work/in-progress/CHG-0242-platform-catalogs/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S1.1 | R1.1 | `internal/agenttools/capability.go HubToolsDefinition` | Define typed catalog records for capability operations/scope/issuers, toolsets with members/effects/via/scope support, and service kinds/dependencies; reuse current definitions and preserve immutable hub-agent-tools versions. |
| S1.2 | R1.2 | `internal/toolhub/api/capability_policy.go` | Generate platform/catalogs/<release>/{capabilities,toolsets,services}.yaml deterministically; validate handlers, prompt contract, docs/tools.md and docs/routines.md parity. Reject unknown names and unsupported executor parameters, and pin catalog versions in every effective passport; add version bump/rebind checks. |

## Acceptance criteria

- [ ] A1: Generated catalogs match real handlers and docs, have stable digests, and reject unknown operations/versions without expanding a pinned space.
- [ ] Inspect operator catalog and one admitted user catalog after tool bind; only allowed entries appear to the user.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestPlatformCatalogParityAndPinnedToolset` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] A new handler is added without catalog/docs/version bump; CI fails instead of exposing it.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Inspect operator catalog and one admitted user catalog after tool bind; only allowed entries appear to the user.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- internal/agenttools/capability.go; docs/tools.md; ADR-0089; M27 #383.

## Out of scope / dependencies

- No new arbitrary plugin runtime or pack acquisition; M27 owns packs.
- Dependencies: M29.0. Stage issues: #424. 
