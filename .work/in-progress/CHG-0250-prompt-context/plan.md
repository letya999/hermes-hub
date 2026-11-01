# CHG-0250: M29.9 Guarantee one read-only system prompt and Hub rules in every managed Hermes turn (M29 stage 9)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R9 -> this plan -> `state.yaml`.
Previous stage: CHG-0249. Next stage: CHG-0251. Evidence: state.yaml.

## Goal

Deliver layered platform/org/user prompt plus effective SOUL/USER/AGENTS and live inventory pointer exactly once.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and ADR-0089; docs/agent-home.md; ADR-0086; M20 #419.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S9.1 Contract and failing regression (R9.1)

Anchors: internal/stack/materialize.go MaterializeSoulEffective; internal/stack/prompt_contract.go; internal/stack/hermes_capability.go; internal/supervisor/ensure.go; internal/runtime/runtime.go; docs/agent-home.md.

Change:
1. Use M29.0 upstream evidence to assemble platform→org→user system prompt, read-only SOUL/USER/AGENTS and compact revision/core/model/discovery guidance without dumping the full catalog.

Tests (red first):
- `TestManagedTurnHasSinglePinnedPromptAcrossEntrypoints` — Each managed turn contains exactly one effective Hub contract and no unauthorized project AGENTS/MEMORY override or writable authority source.

### S9.2 Implementation and consumer migration (R9.2)

Anchors: internal/stack/materialize.go MaterializeSoulEffective; internal/stack/prompt_contract.go; internal/stack/hermes_capability.go; internal/supervisor/ensure.go; internal/runtime/runtime.go; docs/agent-home.md.

Change:
1. Disable or safely fence upstream implicit SOUL/USER/MEMORY/project-rule duplicate loads in managed mode, preserving approved effective skills and M20 vault as sole long-term memory.
2. Pin prompt revision to each run, update on respawn, and check all run paths (chat, routine, voice, API, retry) with safe marker integration; server authorization remains independent.

Tests (red first):
- Extend `TestManagedTurnHasSinglePinnedPromptAcrossEntrypoints` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Repo AGENTS.md or user SOUL.md asks to ignore grant; tool call still denies and prompt source priority is preserved.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A9: Each managed turn contains exactly one effective Hub contract and no unauthorized project AGENTS/MEMORY override or writable authority source.
- Live: Use safe unique markers in pinned Hermes dev runtime across chat/routine/voice-capable request and inspect bounded redacted context evidence.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No upstream Hermes patch or full live catalog in prompt.
- Dependencies: M29.0,M29.2,M29.6.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
