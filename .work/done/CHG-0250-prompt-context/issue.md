## Essence

Deliver layered platform/org/user prompt plus effective SOUL/USER/AGENTS and live inventory pointer exactly once.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R9, A9).
- Stage plan: `.work/in-progress/CHG-0250-prompt-context/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S9.1 | R9.1 | `internal/stack/materialize.go MaterializeSoulEffective` | Use M29.0 upstream evidence to assemble platform→org→user system prompt, read-only SOUL/USER/AGENTS and compact revision/core/model/discovery guidance without dumping the full catalog. |
| S9.2 | R9.2 | `docs/agent-home.md` | Disable or safely fence upstream implicit SOUL/USER/MEMORY/project-rule duplicate loads in managed mode, preserving approved effective skills and M20 vault as sole long-term memory. Pin prompt revision to each run, update on respawn, and check all run paths (chat, routine, voice, API, retry) with safe marker integration; server authorization remains independent. |

## Acceptance criteria

- [ ] A9: Each managed turn contains exactly one effective Hub contract and no unauthorized project AGENTS/MEMORY override or writable authority source.
- [ ] Use safe unique markers in pinned Hermes dev runtime across chat/routine/voice-capable request and inspect bounded redacted context evidence.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestManagedTurnHasSinglePinnedPromptAcrossEntrypoints` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Repo AGENTS.md or user SOUL.md asks to ignore grant; tool call still denies and prompt source priority is preserved.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Use safe unique markers in pinned Hermes dev runtime across chat/routine/voice-capable request and inspect bounded redacted context evidence.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- ADR-0089; docs/agent-home.md; ADR-0086; M20 #419.

## Out of scope / dependencies

- No upstream Hermes patch or full live catalog in prompt.
- Dependencies: M29.0,M29.2,M29.6. Stage issues: #424, #426, #430. 
