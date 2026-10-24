# CHG-0153: memory_search, link graph and maintenance (M20 stage 4)

Chain: ADR-0073 D6 -> SPEC-0070 R4 -> this plan -> `state.yaml`.
Previous stage: CHG-0152. Next stage: CHG-0154.

## Goal

The agent gets a real retrieval tool instead of grep: BM25 over cards and
summaries, link-graph rerank, frontmatter meta, History excluded — plus a
deterministic maintenance pass that keeps the graph, CORE cap and vault
hygiene without any model calls.

## Read before coding

1. `research.md` (iva `memory_search` contract); `internal/agenttools`
   registration style; `internal/vault` card parsing (CHG-0150).

## Slices

### S4.1 FTS index (R4.1)

Anchors: new `internal/vault/index/`; `internal/agenttools` tool file.

Change:
1. `just security` gate for `modernc.org/sqlite` (pure-Go FTS5 driver).
   If accepted: index = FTS5 table `{path,title,meta,body,history,tags,
   status,confidence}`; if rejected: pure-Go BM25 index file — same tool
   contract either way.
2. Per-file mtime+size fingerprint caching; index signature over the
   scanned set; only changed files re-parsed.
3. `## History` section split at parse time (iva `splitHistory`).

Tests (red first):
- `TestIndexBuildsAndRanks` — known fixture, expected order.
- `TestIndexReReadsOnlyChanged` — fingerprint cache hit.
- `TestHistoryExcludedByDefault` / `TestHistoryIncludedOnFlag`.

### S4.2 Link graph + rerank (R4.1)

Anchors: `internal/vault/graph.go`; maintenance writer in S4.3.

Change:
1. `.graph/vault-graph.json` — edges from `[[wiki-links]]`, backlinks,
   hub degree; nightly rebuild by deterministic code.
2. Rerank: BM25 score × (1 + normalized graph centrality), bounded
   boost; ties by recency.

Tests:
- `TestGraphEdgesAndBacklinks`.
- `TestGraphRerankPrefersHub` — equal BM25 → hub card wins.

### S4.3 Scope confinement + soft degrade (R4.2, R4.4)

Anchors: `internal/vault/index/search.go`.

Change:
1. `scopeDirs` resolved under vault root via `os.Root`-style check;
   out-of-root entries dropped silently.
2. Any engine error → substring scan fallback with `degraded: true` in
   the result; call never fails on index trouble.
3. Results: vault-relative `path`, bounded snippet (≤240 chars), meta
   line.

Tests:
- `TestScopeTraversalDropped` — `../..` silently removed.
- `TestEngineFailureDegrades` — corrupted index → fallback + flag.
- `TestResultPathsResolve` — agent file tools can open `path`.

### S4.4 Deterministic maintenance pass (R4.3)

Anchors: `internal/vault/maintain.go`; scheduled alongside rollup.

Change:
1. Rebuild graph, check CORE cap (report, not truncate owner edits),
   collapse duplicated `description` growth (streaming, bounded memory),
   scoped commits, optional `git push` when `remote` configured.
2. Zero model calls; failures logged per subtask, none abort the rest.

Tests:
- `TestMaintainRebuildsGraph` / `TestMaintainCommits`.
- `TestCleanupCollapsesDupDescription`.
- `TestSubtaskFailureIsolated`.

### S4.5 Tool exposure (R4.4)

Anchors: `internal/agenttools` registration + render wiring for
`memory_vault`.

Change:
1. `memory_search` published when `memory_vault` enabled; joins the
   hub's existing tool surface/allowlist machinery.

Tests:
- `TestToolGatedByFeature` — absent without the feature.

## Red-team checklist

- Scope `/etc`, `../../secrets` → silently dropped, result still useful.
- Malformed frontmatter in one card → card skipped by name, others index.
- Graph file deleted mid-run → rebuilt on next maintenance; search
  returns BM25-only with degraded flag.
- Index must never contain `.env`/auth bytes — vault layout test.

## Acceptance

A4.1–A4.4 on dev stack: keyword + graph-hop hits; stale text invisible
without flag; traversal dropped; graph regenerates after edits.

## Out of scope

- Embeddings fusion (stays an OK-side opt-in).
- Cross-user or org search.
