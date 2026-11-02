# CHG-0241: M29.0 Baseline, migration matrix, and pinned Hermes context spike (M29 stage 0)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R0 -> this plan -> `state.yaml`.
Previous stage: none. Next stage: CHG-0242. Evidence: research.md.

## Goal

Map every current authority and empirically prove how the pinned Hermes build loads system, SOUL, USER, MEMORY, project AGENTS, and skills before changing the runtime contract.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/adr/ADR-0089-live-tool-inventory.md; docs/adr/ADR-0086-memory-vault-openknowledge.md; #418; #383; #402.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S0.1 Contract and failing regression (R0.1)

Anchors: internal/stack/config.go Settings; internal/stack/render_env.go RenderEnvironment; internal/stack/materialize.go MaterializeSoulEffective; internal/supervisor/ensure.go; internal/runtime/runtime.go.

Change:
1. Record source→consumer→persisted-state ownership for scope/agent/workspace/settings/models/profiles/grants/ToolHub/skills/channel files; mark fields to move, preserve, or retire.

Tests (red first):
- `TestManagedPromptSourcesInventory` — Complete migration matrix and reproducible Hermes loading evidence; no ambiguous field owner or unproven suppress/inject claim.

### S0.2 Implementation and consumer migration (R0.2)

Anchors: internal/stack/config.go Settings; internal/stack/render_env.go RenderEnvironment; internal/stack/materialize.go MaterializeSoulEffective; internal/supervisor/ensure.go; internal/runtime/runtime.go.

Change:
1. Use a managed dev runtime to test each upstream context-loading path and `--ignore-rules`; save redacted evidence and fail-closed decision in research.md.
2. Reconcile ADR-0092/SPEC-0091 with pinned Hermes behavior and existing M20/M27/M28 contracts before downstream changes.

Tests (red first):
- Extend `TestManagedPromptSourcesInventory` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- A project AGENTS.md or MEMORY.md attempts to override a platform restriction; record actual load order and server-side denial.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A0: Complete migration matrix and reproducible Hermes loading evidence; no ambiguous field owner or unproven suppress/inject claim.
- Live: Run one pinned managed Hermes turn with distinct safe markers in each source and inspect redacted prompt/trace receipt.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No production behavior change or upstream patch.
- Dependencies: none.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
