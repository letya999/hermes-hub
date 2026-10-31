# CHG-0206: Night rollup — gated consolidation, directives, journal (M20 stage 5)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D4/D5/D14 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R5 -> this plan ->
`state.yaml`. Evidence: `research.md`, `../CHG-0204-vault-layout/spike-m20.md`.
Previous stage: CHG-0238. Next stage: CHG-0208.

## Goal

A hub-owned nightly job turns raw days into durable knowledge behind the
gates that make consolidation trustworthy: promotion gate (provenance +
kind), bounded-loss gate (prior state preserved, append-only fallback),
Compiled Truth hash-check, directive supersede-in-place, `REVIEW.md`
journal + morning digest, and intent→routine proposals.

## Read before coding

1. `research.md` (iva night semantics); ADR-0086 D4/D5/D14; SPEC-0085 R5.
2. `internal/supervisor` scheduling; `internal/routines`; model-relay
   (`internal/runtime`, serviceauth); `internal/memory` prov.db +
   `PromotionEligible` (CHG-0205); `internal/vault` writer (CHG-0204).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only. Model is a JSON oracle via model-relay;
  fixtures, never live calls in tests.
- `just check` before handoff.

## Slices

### S5.1 Steps 1–4 + promotion gate (R5.1–R5.3)

Anchors: `internal/memory/night` (new package); `internal/memory`
prov.db `PromotionEligible`.

Change:
1. Pipeline: day summary + card refs + evidence facts → dated fact
   appends + Compiled Truth proposal → bilateral links → bounded CORE
   edit. All IO in Go; model only returns lenient-parsed JSON.
2. Promotion gate as precondition: only `owner`/`agent` + `Kind=
   interactive` entries feed the model's fact candidates; everything
   else is filtered before the prompt is built.
3. Evidence quotes verified verbatim against leaf text — a mutated
   quote rejects the fact.

Tests (red first):
- `TestPromotionGateExcludesUntrusted`, `TestPromotionGateExcludesKinds`,
  `TestMutatedQuoteRejected`, `TestDaySummarySeeded`.

### S5.2 Directives (R5.5)

Anchors: `internal/memory/night/core.go`.

Change:
1. CORE `## Directives`: supersede-in-place (new dated directive
   replaces the old line on the same key), never-evict class
   (health/safety pinned).

Tests:
- `TestDirectiveSupersedeInPlace`, `TestDirectiveNeverEvict`.

### S5.3 Bounded-loss gate (R5.4)

Anchors: `internal/memory/night` rewrite path.

Change:
1. Rewrite accepted only if dropped entries ≤ X% of prior; else reject,
   preserve prior state, append-only fallback, journal the rejection.

Tests:
- `TestBoundedLossRejects`, `TestBoundedLossPreservesPrior`,
  `TestBoundedLossFallbackAppendOnly`.

### S5.4 REVIEW.md + morning digest (R5.6)

Anchors: `internal/memory/night/journal.go`; owner-brief path
(`internal/communication` proactive/brief).

Change:
1. `vault/REVIEW.md` appended per run: every promotion/skip/conflict,
   human-readable.
2. Short "what I remembered" digest queued through the existing
   owner-brief delivery path.

Tests:
- `TestReviewJournalEntries`, `TestDigestQueued`.

### S5.5 Intent → routine proposals (R5.7)

Anchors: `internal/memory/night/intent.go`; proposals surface (M23
assignments draft path).

Change:
1. "remind me Friday" lines → routine/assignment proposal records for
   owner approval; never facts, never activated.

Tests:
- `TestIntentBecomesProposal`, `TestIntentNeverFact`.

### S5.6 Cache, resume, budget (R5.8, R5.9)

Anchors: `runtime/memory/night/` idempotent cache; caps.

Change:
1. Resume-from-step cache; changed already-processed entry halts the
   day; ≤40 calls / ≤300k input tokens stops cleanly; `--dry-run`.
2. Fold-up: weekly needs all 7 day states incl. no-data; monthly = full
   in-month weeks + edge days; yearly = 12 months.

Tests:
- `TestResumeFromStep`, `TestChangedEntryHalts`, `TestBudgetStopsClean`,
  `TestSecondRunNoop`, `TestWeeklyNeedsSeven`, `TestDSTEdge`.

## Red-team checklist

- An untrusted day tries every promotion path — none works.
- Evidence quote subtly mutated → rejected.
- Owner edits a card mid-rollup → owner version wins.
- Rollup writes outside allowed zones → writer denies.
- DST / 04:00 edge cases.

## Acceptance

A5.1–A5.9 on the dev stack: seeded day → summary+facts+digest;
owner card edit preserved with `truth_pending`.

## Out of scope

- Vector/graph consolidation, org-level rollup, auto-activating
  routines.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
