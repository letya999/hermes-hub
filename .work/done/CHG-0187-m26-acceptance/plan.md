# CHG-0187: M26 live acceptance & docs (M26 stage 5)

Chain: `specs/active/SPEC-0082-image-role-budgets.md` R5/A5 -> this plan ->
`state.yaml`. GitHub issue: #380. Previous stage: `CHG-0186`.

## Goal

Close the milestone with live dev-stack evidence: voice through services,
browser through the sidecar, forbidden-binary budgets enforced, sizes and
CVE counts recorded, docs current.

## Read before coding

1. `docs/operations.md`, `docs/architecture.md`, `docs/tools.md`,
   `docs/integrations.md`, `docs/index.md`, `justfile` (`check`, `docker-check*`).
2. SPEC-0082 A0–A5.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- `just check` before handoff; Docker gates per AGENTS.md.
- Evidence rules: no creds, tokens, profiles or transcripts in state.yaml.

## Slices

### S5.1 Live acceptance run (R5.1)

Change:
1. `just check`, then `just docker-pull` + `just docker-check-prebuilt` or
   `just docker-check` per AGENTS.md gate rules.
2. Live: telegram voice note -> hub-stt -> transcript; TTS reply;
   playwright-mcp navigate/screenshot via sidecar; noVNC auth check;
   Meet smoke if creds exist.
3. Forbidden-binary + budget gate on every role image.

Tests (red first):
- `TestRoleImagesStayInBudget` regression on the images list.

### S5.2 Evidence + docs (R5.2)

Change:
1. Before/after sizes + Trivy counts in `state.yaml`.
2. Update architecture/operations/tools/integrations docs + index.md.

## Red-team checklist

- Rollback verified: `hubctl up` with previous tags restores the same
  service set.
- Only narrow image cleanups; no broad prunes.

## Acceptance

- A5: all A0–A5 evidenced on the dev stack; coverage >= 85%.

## Out of scope

- M27 capability packs and node removal.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #380 closed; folder moved to `.work/done/`; docs/index updated.
