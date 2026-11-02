## Essence

Map every current authority and empirically prove how the pinned Hermes build loads system, SOUL, USER, MEMORY, project AGENTS, and skills before changing the runtime contract.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R0, A0).
- Stage plan: `.work/in-progress/CHG-0241-baseline-upstream/plan.md` and `state.yaml`.
- Research: `.work/in-progress/CHG-0241-baseline-upstream/research.md`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S0.1 | R0.1 | `internal/stack/config.go Settings` | Record source→consumer→persisted-state ownership for scope/agent/workspace/settings/models/profiles/grants/ToolHub/skills/channel files; mark fields to move, preserve, or retire. |
| S0.2 | R0.2 | `internal/runtime/runtime.go` | Use a managed dev runtime to test each upstream context-loading path and `--ignore-rules`; save redacted evidence and fail-closed decision in research.md. Reconcile ADR-0092/SPEC-0091 with pinned Hermes behavior and existing M20/M27/M28 contracts before downstream changes. |

## Acceptance criteria

- [ ] A0: Complete migration matrix and reproducible Hermes loading evidence; no ambiguous field owner or unproven suppress/inject claim.
- [ ] Run one pinned managed Hermes turn with distinct safe markers in each source and inspect redacted prompt/trace receipt.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestManagedPromptSourcesInventory` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] A project AGENTS.md or MEMORY.md attempts to override a platform restriction; record actual load order and server-side denial.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Run one pinned managed Hermes turn with distinct safe markers in each source and inspect redacted prompt/trace receipt.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/adr/ADR-0089-live-tool-inventory.md; docs/adr/ADR-0086-memory-vault-openknowledge.md; #418; #383; #402.

## Out of scope / dependencies

- No production behavior change or upstream patch.
- Dependencies: none. This stage resolves overlap with M20, M27 and M28.
