# CHG-0200: Skills platform design record (M24 stage 1)

Chain: `docs/adr/ADR-0085-agent-skills-platform.md` D1–D13 ->
`specs/active/SPEC-0084-agent-skills-platform.md` R1 -> this plan ->
`state.yaml`. Evidence: `research.md`.
Next stages: CHG-0201..CHG-0199 (one per M24.2..M24.13, assigned at implementation).

## Goal

Freeze the skills-platform architecture before code: ADR + SPEC accepted,
indexes consistent, and each upstream assumption verified against pinned
Hermes `869228c` (research.md §3).

## Read before coding

1. ADR-0085, SPEC-0084, `research.md` §1 (current state) and §3 (spike)
2. Issue #356 (source of D1–D12; numbers remapped — research.md §5)

## Preconditions and rules

- No code change in this stage.
- Do not rewrite accepted records (SPEC-0041/0042 get appended notes only).
- Numbers: milestone text says ADR-0083/SPEC-0082/CHG-0182 — taken by M26;
  M24 uses ADR-0085 / SPEC-0084 / CHG-0200+.

## Slices

### S1.1 Decision record (R1.1, R1.2)

Anchors: `docs/adr/ADR-0083-*.md` (format), `docs/adr/index.md`,
`specs/index.md`, `.work/index.md`.

Change:
1. `research.md` — current-state audit, harness survey, pin spike.
2. `docs/adr/ADR-0085-agent-skills-platform.md` — D1–D13.
3. `specs/active/SPEC-0084-agent-skills-platform.md` — R1–R12 + A2–A13.
4. Index rows in `docs/adr/index.md`, `specs/index.md`, `.work/index.md`.
5. Superseding notes appended to SPEC-0041 (CP-07 Skills row) and
   SPEC-0042 (skills clause).

Tests (red first): `go run ./cmd/devcheck docs` — green.

### S1.2 Pin verification spike (R1.3)

Anchors: `docker/Dockerfile:62` (pin), `.work/tmp/hermes-src` (fetch target).

Change:
1. Fetch `869228cab4a8276d3b4c78da9d9939670c47bd0f` shallow into ignored
   `.work/tmp/`; verify the six assumptions statically; record commands +
   observed output in `research.md` §3.
2. Runtime legs (spawn behavior, curator over a day) stay for M24.13 live
   acceptance — recorded as limitations, not claimed.

## Red-team checklist

- ADR must not describe an upstream control that does not exist at the pin
  (each claim in D3/D8/D9/D13 cites the spike row that proves it).
- No secrets/personal data in research.md (names-only rule for space paths).

## Acceptance

- A1 (SPEC-0084 R1.1–R1.3): docs merged, `devcheck docs` green, spike table
  complete, superseding notes appended.

## Out of scope

- Any code change (starts in M24.2 / CHG-0201).
- Docker builds; runtime verification beyond static source checks.

## Done checklist

- [x] Tests green: `devcheck docs` (see state.yaml validation).
- [x] Spike recorded with commands/output.
- [x] Indexes + superseding notes updated.
- [ ] Issue #356 commented with evidence and closed; folder to `.work/done/`.
