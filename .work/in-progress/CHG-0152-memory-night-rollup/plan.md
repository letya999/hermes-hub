# CHG-0152: Memory-night rollup (M20 stage 3)

Chain: ADR-0073 D4/D5 -> SPEC-0070 R3 -> this plan -> `state.yaml`.
Previous stage: CHG-0151. Next stage: CHG-0153.

## Goal

A hub-owned nightly job turns raw days into durable knowledge: day
summary, evidence-linked card facts, Compiled Truth under optimistic
hash check, bilateral links, bounded CORE.md edits — deterministic Go
orchestration, model as proposal oracle via the space's model-relay.

## Read before coding

1. `research.md` (iva night semantics); `internal/supervisor` scheduling;
   `internal/routines`; model-relay client (`internal/runtime/
   model_relay.go`, serviceauth); `internal/vault` from CHG-0150/0151.

## Slices

### S3.1 Scheduler + job skeleton (R3.1)

Anchors: new `internal/vault/night/` package; scheduler anchor in
`internal/supervisor` (hub-owned per-space timers).

Change:
1. Per-space cron evaluation in the space's timezone; `memory.rollup_time`
   (default `04:00`) in `agent.yaml` `memory:` map.
2. Job = ordered step runner; each step: read → model call (JSON
   proposal) → validate → write/commit → checkpoint.
3. Model access only via model-relay client with the space's model
   config; no direct provider keys in job env.

Tests (red first):
- `TestRollupTimeParsed` — HH:mm validation, default.
- `TestStepOrder` — steps run a→b→c→d, later step skipped on earlier
  failure.
- `TestModelViaRelayOnly` — no provider env keys in job spec.

### S3.2 Steps a–d (R3.2)

Anchors: `internal/vault/night/steps.go`; `internal/vault/cards.go`.

Change:
1. (a) `SummarizeDay`: parse leaf → propose `{summary, card_refs, facts[]}`;
   every fact requires `evidence` quoting an owner line; writes
   `summaries/daily/YYYY-MM-DD.md`.
2. (b) `UpdateCards`: append `- YYYY-MM-DD: fact · [[daily/…]] HH:MM`
   (append-only); propose new Compiled Truth; apply only if file hash
   unchanged since read (D5); owner-conflict → `truth_pending` marker,
   replaced truth → `## History`.
3. (c) `LinkCards`: create reciprocal `[[links]]` only when both cards
   exist.
4. (d) `EditCore`: bounded CORE.md proposal sourced from owner
   quotations only; enforced ≤3600; old content → `CORE.history.md`.

Tests:
- `TestFactRequiresOwnerEvidence` — non-owner evidence rejected.
- `TestTruthHashCheck` — concurrent owner edit wins, marker set.
- `TestLinksBilateral` / `TestLinksSkipsMissing`.
- `TestCoreEditBoundedAndArchived`.

### S3.3 Caps + usage gate (R3.3)

Anchors: `internal/vault/night/budget.go`.

Change:
1. `Budget{MaxCalls: 40, MaxInputTokens: 300000}` configurable via
   `memory:` map; estimated tokens before each call; exceeding → clean
   stop, checkpoint recorded; never stop mid-write.

Tests:
- `TestBudgetStopsBeforeNextCall` — no partial step.
- `TestBudgetConfigurable`.

### S3.4 Idempotent cache + resume (R3.4, R3.6)

Anchors: `internal/vault/night/cache.go`; state under
`spaces/<id>/runtime/memory/night/`.

Change:
1. Per-day cache: completed steps, input fingerprint (file hashes).
2. Restart resumes at first uncompleted step; a changed already-
   processed input halts the day with an explicit `needs_owner` record.
3. Day readiness recorded only after summary commit; transport/provider
   failure consumes no schema-failure attempt.

Tests:
- `TestResumeFromSavedStep` — kill at step b → rerun starts at b.
- `TestChangedProcessedInputHalts`.
- `TestReadinessOnlyAfterCommit`.

### S3.5 Fold-up (R3.5)

Anchors: `internal/vault/night/fold.go`.

Change:
1. weekly: all 7 day states incl. explicit `no-data`; monthly: full
   in-month weeks + in-month edge days; yearly: 12 months.

Tests:
- `TestWeeklyNeedsSevenStates` — 6 days + implicit gap → no weekly.
- `TestMonthlyEdgeWeeks` / `TestYearlyNeedsTwelve`.

## Red-team checklist

- Model proposes a fact quoting a non-existent daily line → rejected
  (evidence must resolve to a real leaf line).
- Model proposes CORE edit quoting nothing → rejected.
- Prompt-injection text inside a leaf reaches the model only inside the
  untrusted fence (leaves carry the R2 marker through).
- Two spaces never share cache/vault paths (per-space roots only).

## Acceptance

A3.1–A3.4 on dev stack with a fixture model response file or the dev
model: seeded day → summary+fact; killed run resumes; owner edit during
run → `truth_pending`; 7-day week → weekly.

## Out of scope

- Embeddings/semantic ranking inside rollup.
- Cross-vault/org rollup.
