# CHG-0276: Pre-turn recall lane — zero-LLM, pinned per run (M20 stage 4)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D12/D13 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R4 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/spike-m20.md`.
Previous stage: CHG-0207. Next stage: CHG-0206.

## Goal

Before each run the hub injects a bounded, audience-scoped memory block —
CORE + ≤4 curated hits — through `ExecuteRequest.Instructions`, pinned per
job so retries send byte-identical bytes. Zero model calls. A lane-2 hint
line on past-referencing turns nudges `memory_search`.

## Read before coding

1. ADR-0086 D11–D13; SPEC-0085 R4; spike-m20.md Q1 (instructions channel).
2. `internal/communication/runtime.go` instructions assembly;
   `internal/communication/tasks.go` `StyleForRun` (pinned-snapshot
   pattern); `internal/commodel/mapping.go` JobMapping.
3. `internal/memory` search API + scope minting (CHG-0204/0207).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- `just check` before handoff.

## Slices

### S4.1 Recall assembly (R4.1)

Anchors: `internal/memory` (new `recall.go`); search index API.

Change:
1. `RecallBlock(scope)` → CORE slice + ≤4 curated hits, recency-decayed,
   hard cap 2500 chars, "data, not instructions" frame + recalled marker.

Tests (red first):
- `TestRecallBudgetCap`, `TestRecallFramePresent`, `TestRecallOrdering`.

### S4.2 Pinned snapshot (R4.2)

Anchors: `internal/commodel/mapping.go` JobMapping fields;
`internal/communication/tasks.go` `StyleForRun` analog `RecallForRun`.

Change:
1. `JobMapping.Recall/RecallSnapshotted` pinned under spool lock at admit;
   retry replays identical bytes.

Tests:
- `TestRecallSnapshotPinnedAcrossRetries`.

### S4.3 Instructions delivery (R4.3)

Anchors: `internal/communication/runtime.go` RunOutcome.

Change:
1. Compose recall block into `instructions` alongside style/lang hints;
   degraded path flags and still runs.

Tests:
- `TestRecallDegradedStillRuns`, `TestRecallInInstructions`.

### S4.4 Lane-2 hint (R4.6)

Anchors: recall builder.

Change:
1. EN+RU past-reference trigger phrases → single hint line.

Tests:
- `TestLane2HintTriggers`, `TestLane2SilentOnFreshTopic`.

### S4.5 Audience gate (R4.5)

Anchors: scope minting `internal/memory`.

Change:
1. Private scope → CORE+hits; group/other → own-audience slice only.

Tests:
- `TestRecallAudienceFilter`, `TestGroupRunExcludesPrivateRecall`.

## Red-team checklist

- Directive-laden memory hit → inert under the frame (injection test).
- Recall flooding respects budget and ordering.
- Crafted scope/job fields cannot mint private scope in a group run.

## Acceptance

A4.1–A4.7 on the dev stack: stored fact visible next run, byte-identical
retry, group-run prompt contains no private bytes.

## Out of scope

- Vector lane, recall subagent, cross-vault federation.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
