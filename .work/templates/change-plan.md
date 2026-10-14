# CHG-<NNNN>: <title> (M<N> stage <k>)

Chain: `docs/adr/ADR-<NNNN>-<name>.md` D<x> -> `specs/active/SPEC-<NNNN>-<name>.md` R<k>
-> this plan -> `state.yaml`. Evidence: `research.md`.
Previous stage: `CHG-<NNNN>`. Next stage: `CHG-<NNNN>`.

## Goal

<one paragraph: user-visible outcome of this stage>

## Read before coding

1. <docs, ADRs, specs in order>
2. <related open issues>

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates when image/runtime code changes.

## Slices

### S<k>.1 <name> (R<k>.<n>)

Anchors: `<file>` `<function>`; `<file>` `<function>`.

Change:
1. <concrete step>

Tests (red first):
- `Test<Scenario>`: <what it proves>

## Red-team checklist

- <abuse case and expected safe behaviour>

## Acceptance

<SPEC acceptance IDs and the exact live procedure>

## Out of scope

<explicit list>

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates if needed.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
