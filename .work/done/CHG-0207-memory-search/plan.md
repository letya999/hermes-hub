# CHG-0207: memory_search — BM25 + link graph (M20 stage 3)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D6 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R3 -> this plan ->
`state.yaml`. Evidence: `research.md`.
Previous stage: CHG-0205. Next stage: CHG-0276.

## Goal

`memory_search` — pure-Go BM25 over `daily/` + `cards/` + `summaries/`
with `.graph` link expansion, recency decay, scope confinement and
honest degraded states. Every hit carries an evidence ref. No model, no
new dependency, no vector lane.

## Read before coding

1. `research.md` (iva `memory_search` contract); ADR-0086 D6; SPEC-0085 R3.
2. `internal/agenttools` registration style; `internal/vault` card
   parsing + prov.db (CHG-0204/0205); `internal/memory` scope minting.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- `just check` before handoff.

## Slices

### S3.1 Tokenizer + BM25 (R3.1)

Anchors: `internal/memory/search` (new package).

Change:
1. Unicode tokenizer (word-boundary segmentation, lowercase, RU/EN —
   ё→е normalization, no stemmer dependency) + BM25 scoring over
   per-file documents split into line-range chunks; recency decay on
   score.

Tests (red first):
- `TestBM25RankingSanity`, `TestUnicodeTokenizationRU`,
  `TestRecencyDecayOrdering`.

### S3.2 Lazy index (R3.2)

Anchors: `internal/memory/search/index.go` — `runtime/memory/index/`
outside the vault.

Change:
1. mtime+size cache per file; per-scope rebuild on first stale query;
   index files contain only opaque scope keys.
2. `Invalidate(path)` on writer commits.

Tests:
- `TestIndexCacheInvalidation`, `TestIndexRebuildOnStale`,
  `TestIndexNoRawIdentifiers`, `TestOversizedBinarySkipped`.

### S3.3 Graph rerank (R3.1)

Anchors: `internal/vault` `.graph/vault-graph.json` (rebuilt by the
maintenance pass); card `[[links]]` parse.

Change:
1. Card neighborhood link expansion boosts hits one hop out.

Tests:
- `TestGraphNeighborBoost`, `TestGraphMissingTolerated`.

### S3.4 Filters + safe defaults (R3.4)

Anchors: search API; prov.db.

Change:
1. Audience confinement (private hits invisible to group scope);
   superseded/forgotten excluded unless `history:true`; untrusted hits
   flagged `untrusted:true` and never eligible for the R4 recall path;
   `kinds:` filter maps to prov.db kinds.

Tests:
- `TestScopeIsolationMatrix`, `TestHistoryFlagSemantics`,
  `TestUntrustedFlaggedNotInRecallLane`, `TestKindFilter`.

### S3.5 Tool surface (R3.3, R3.5)

Anchors: `internal/agenttools` tool registration + hub-agent-tools
catalog; `docs/tools.md`, `docs/routines.md`,
`internal/stack/prompt_contract.go`.

Change:
1. `memory_search(query, limit, from, to, kinds)` returning hits with
   evidence refs (file + date + line range) and a status of
   empty/partial/degraded — degraded never swallows the turn.
2. Contract changeset in one commit: description + prompt-contract +
   docs + catalog bump.

Tests:
- `TestSearchToolEvidenceRefs`, `TestSearchDegradedHonest`,
  `TestSearchEmptyVsUnavailable`, `TestSearchPerf10k` (p95 < 50 ms on a
  10k-entry fixture).

### S3.6 Maintenance (R3.6)

Anchors: `hubctl memory search --reindex`; `internal/stack/doctor.go`.

Change:
1. Reindex command; doctor reports stale index.

Tests:
- `TestReindexCommand`, `TestDoctorStaleIndex`.

## Red-team checklist

- Query-side scope injection cannot cross audience/space boundaries.
- Crafted leaf (keyword stuffing, huge file) degrades gracefully.
- Binary/oversized vault content cannot wedge the index.
- Index files cannot leak raw identifiers.

## Acceptance

A3.1–A3.6 on the dev stack: leaf search finds the right day; evidence
links resolve; deleted index rebuilds and reports degraded.

## Out of scope

- Embeddings/vector lane, cross-vault federation, recall injection.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
