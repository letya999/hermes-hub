# CHG-0240: Leaf retention + active-day fold — night prune (M20 follow-up)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` (new amendment) ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` (new R5.x/R7.x) ->
this plan -> `state.yaml`. Issue: #423.
Previous stage: CHG-0239 (M20.11).

## Goal

Daily leaves are an unbounded audit tape — every turn+reply of every kind
plus compaction flushes land verbatim, and nothing ages out. Add two
deterministic night-tick lanes that run only after `night.Run` + folds:

- **FOLD**: a completed day whose leaf exceeds `memory_leaf_fold_bytes` gets
  its non-promotion-eligible block bodies stubbed in place — mem:id headers
  and interactive owner/agent blocks stay verbatim. An oversized eligible
  day also asks the same proposal call for `long_summary`, so detail lands
  in `summaries/daily/` before bytes are cut.
- **EXPIRE**: leaves older than `memory_leaf_retention_days` are removed via
  governed commit and their prov.db rows drop — only when the day-state is
  complete and `summaries/daily/<day>.md` exists. Anything unverified is
  kept and reported, never deleted.

## Read before coding

1. `internal/memory/night` day-state (`complete()`), journal→`memory/REVIEW.md`,
   `writeDaySummary`, `proposal` JSON; `nightjob.go` tick order.
2. `internal/memory/vacuum` — `stripBlocks`, `git rm`+commit, `dropProvRows`
   shape to mirror (not to reuse blindly).
3. `internal/vault` `Writer.Apply` (WantSHA retry, ActorSystem) and the daily
   leaf block format (`formatDailyBlock`, `<!-- mem:id=… -->`).
4. `internal/stack/config.go` Settings + `rights.go` classification;
   `compose.go` HUB_MEMORY_* env render; `internal/communication/config.go`.

## Preconditions and rules

- Both knobs default OFF (opt-in — the `rollup_time` pattern: empty/absent
  disables). Validation: retention 0 or >=2, fold_bytes >=0.
- Fail-closed only: no complete day-state + no day summary => keep + report.
- Fold is idempotent (folded marker); expire is idempotent (file absent).
- Never touch today/yesterday's leaf; never fold an unrolled day.
- Red test first; own coverage >=85%; `just check` before handoff.

## Slices

### S12.1 Settings + env plumbing

`MemoryLeafRetentionDays`, `MemoryLeafFoldBytes` in Settings + Validate +
rights `""`; compose renders `HUB_MEMORY_LEAF_RETENTION_DAYS` /
`HUB_MEMORY_LEAF_FOLD_BYTES` when >0; `communication.Config` reads them.

Tests: `TestLeafRetentionValidation`, `TestLeafKnobsRenderEnv`,
`TestLeafKnobsConfigEnv`.

### S12.2 internal/memory/prune — Scan/Apply + gates

`prune.Options{RetentionDays, FoldBytes, Now, TZ}`; `Scan` -> `Report`
{folded candidates, expired candidates, kept (age-eligible but
unverified)}; `Apply` executes: fold via Writer (stub non-eligible bodies,
verify mem:ids preserved + eligible blocks byte-identical), expire via
`git rm`+commit + prov row drop by the leaf's mem:id set; journal lines to
`memory/REVIEW.md` under `## prune <dayKey>`.

Tests: `TestFoldOnlyCompleted`, `TestFoldStubsAuditKeepsEligible`,
`TestFoldIdempotent`, `TestFoldVerifyIDs`, `TestExpireRequiresSummary`,
`TestExpireDropsLeafAndProv`, `TestExpireKeepsUnrolledOld`,
`TestNeverTouchesRecentLeaf`, `TestReportKeepsUnverified`.

### S12.3 long_summary for big days

`proposal.long_summary`; `Options.FoldBytes` -> prompt asks for it when
eligible bytes exceed the knob; `writeDaySummary` prefers long_summary.

Tests: `TestBigDayRequestsLongSummary`, `TestLongSummaryWritten`.

### S12.4 Night-tick wiring + digest + REVIEW

`tickNightRollup` runs `prune.Scan`/`Apply` after `nightFolds`; prune lines
append to the night digest (`memory.prune-digest` locale key, 4 langs);
REVIEW.md gains `## prune <dayKey>` sections via Writer.

Tests: `TestNightTickPrunes`, `TestDigestIncludesPruneLine`,
`TestReviewPruneSection`, `TestLocaleParity` (existing harness covers).

### S12.5 hubctl memory prune --dry/--apply

`runMemory` case `prune`: prints JSON scan report; `--apply` executes.

Tests: `TestMemoryPruneCLI` (dry + apply).

### S12.6 Docs + gates

SPEC-0085 R5.10/R7.5 rows; ADR-0086 amendment (D20); operations.md prune
paragraph; spaces.md settings bullets; state.yaml; index row; coverage.
