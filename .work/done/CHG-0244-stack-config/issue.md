## Essence

Give stack.yaml and config.yaml one responsibility each and render Compose/supervision from their validated pair.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R3, A3).
- Stage plan: `.work/in-progress/CHG-0244-stack-config/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S3.1 | R3.1 | `internal/stack/config.go Settings` | Move service identity/instances/modes to stack and channel/memory/supervision/port/runtime choices to config, including vault/external memory, nightly rollup, dreams and protected critical stack watchers; keep secret bytes in existing secure sources. |
| S3.2 | R3.2 | `internal/supervisor/ensure.go` | Validate service dependencies, proxy references, channel voice dependencies, port collisions, managed-mode constraints and service catalog pins before generation. Render compose, service env and supervisor selection from one effective revision; preserve dev/prod isolation and existing shared infra behavior. |

## Acceptance criteria

- [ ] A3: A user can inspect which services run and how they are configured; missing service or conflicting port fails before any compose change.
- [ ] Protected critical stack watchers survive user changes; user-created watchers remain ordinary granted tools, not service config authority.
- [ ] Render dev stack for shared infra and a secondary user, inspect compose and endpoint health without leaking env values.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestStackConfigRejectsMissingServiceAndPortCollision` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] A user config attempts to enable a forbidden service or expose a private port; ceiling rejects it.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Render dev stack for shared infra and a secondary user, inspect compose and endpoint health without leaking env values.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/architecture.md; docs/spaces.md; docs/operations.md; M28 #402/#405.

## Out of scope / dependencies

- No M28 shared edge redesign and no arbitrary container creation.
- Dependencies: M29.2. Stage issues: #426. 
