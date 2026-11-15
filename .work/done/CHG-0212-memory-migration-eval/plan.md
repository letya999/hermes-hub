# CHG-0212: Memory eval gate + native import + vacuum (M20 stage 7)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D10/D16 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R7 -> this plan ->
`state.yaml`.
Previous stage: CHG-0208. Next stage: CHG-0209.

## Goal

Prove the wave-1 memory behavior with a deterministic eval fixture, add
the one-time `MEMORY.md`/`USER.md` import into reviewable cards, run the
small live eval through model-relay, and ship `memory vacuum` hygiene.
Honcho export moved to CHG-0277.

## Read before coding

1. ADR-0086 D10/D16; SPEC-0085 R7; iva `memory-eval` fixture pattern
   (research.md); LongMemEval categories.
2. `internal/skilleval` HTTPRunner pattern; `internal/vault` cards +
   writer; `internal/memory` search/recall; `cmd/hubctl` memory verbs.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only. No live-model claims from mocks.
- `just check` before handoff.

## Slices

### S7.1 Native importer (R7.1)

Anchors: `internal/memory/import` (new); cards writer.

Change:
1. Parse `MEMORY.md`/`USER.md` → draft cards + directive candidates,
   `source: migrated`; review-then-commit — nothing lands silently;
   crafted prompt-injection imports as inert data.

Tests (red first):
- `TestImportReviewableDrafts`, `TestImportInjectionInert`,
  `TestImportIdempotent`.

### S7.2 Deterministic eval fixture (R7.2)

Anchors: `internal/memory/eval` (new); testdata seeded vault.

Change:
1. Seeded vault + scripted turn sequence + assertions (no model):
   recall@k ≥ 0.9, supersede correctness, untrusted non-promotion,
   background non-capture, audience isolation, forget persistence.
   Same seed → same assertions; fixture content cannot leak into a
   production index (isolated temp root).

Tests:
- `TestEvalFixtureDeterministic`, `TestEvalRecallBound`,
  `TestEvalIsolation`, `TestEvalForgetSilence`,
  `TestEvalBackgroundExcluded`, `TestEvalUntrustedNotPromoted`.

### S7.3 Live eval runner (R7.3)

Anchors: `internal/skilleval` HTTPRunner pattern; `hubctl memory eval`.

Change:
1. ~40 RU/EN questions across LongMemEval categories through
   model-relay; judged against expected evidence refs; results written
   to `.work/tmp/` report — mocks never count as live.

Tests:
- `TestLiveEvalReportShape` (offline); live run recorded pending —
  requires live stack (deferred — no-docker scope).

### S7.4 `memory vacuum` (R7.4)

Anchors: `hubctl memory vacuum`; prov.db supersede records.

Change:
1. Prune superseded/stale per policy; dry-run reports accurately;
   never deletes without `--apply`; cannot remove non-superseded
   content; git history always recoverable.

Tests:
- `TestVacuumDryRunAccurate`, `TestVacuumNeedsApplyFlag`,
  `TestVacuumKeepsLiveContent`.

## Red-team checklist

- Crafted `MEMORY.md` prompt-injection imports as inert data.
- Fixture content cannot leak into the production index.
- Vacuum cannot remove non-superseded content.

## Acceptance

A7.1–A7.5. Live eval + importer-on-real-space recorded pending —
requires live stack (deferred — no-docker scope).

## Out of scope

- Honcho export (CHG-0277), public-benchmark reproductions, vector
  lane eval.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
