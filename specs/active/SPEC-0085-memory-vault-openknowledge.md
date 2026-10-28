---
description: Frozen requirements for the M20 memory vault (leaves, cards, summaries, CORE, nightly rollup, search, write contract) and the opt-in OpenKnowledge UI surface.
last_verified: 2026-10-24
---
# SPEC-0085: Memory vault and OpenKnowledge knowledge surface

Frozen: 2026-10-24. Milestone M20. Decisions: ADR-0086 D1–D10.
Research: `.work/in-progress/CHG-0204-vault-layout/research.md`.
Ported-from reference: smixs/iva-agent `docs/memory.md`,
`agent/lib/{vault,card,memory-night}*`, `agent/tools/{memory_search,write_card}.ts`.

## R1 — Vault layout and CORE injection (CHG-0204)

- R1.1 `memory_vault` is an opt-in entry in the space `tools:`/feature
  vocabulary. When enabled, `hubctl render` creates `spaces/<id>/vault/`
  as a git repository with the D2 layout (`memory/daily|cards|summaries|…`,
  `CORE.md`, `CORE.history.md`, `schema.json`, `.graph/`, `attachments/`),
  mounts it into the runtime at `/vault`, and renders no other effect.
- R1.2 `schema.json` validates card frontmatter (type, status, confidence,
  source, tags, aliases, created/updated) using `google/jsonschema-go`;
  invalid cards are reported, never silently repaired.
- R1.3 `CORE.md` is capped at 3600 chars and is materialized into the
  rendered agent prompt context alongside `soul-effective`; the
  materialization re-reads the current file at each render and each
  rollup, never a stale snapshot.
- R1.4 Enabling `memory_vault` on a space without a vault scaffolds the
  template idempotently; existing files are never overwritten.
- R1.5 A disabled or absent feature renders no mount, no prompt fragment
  and no scheduled job (deny-wins).

Acceptance: A1.1 render+init on a fresh space yields the template tree and
a git repo with the initial commit; A1.2 CORE content appears in the
generated prompt context; A1.3 second init is a no-op; A1.4 feature off →
doctor/render show zero vault effects.

## R2 — Daily leaf ingest (CHG-0205)

- R2.1 The communication plane appends every accepted owner turn and
  agent reply verbatim to `vault/memory/daily/YYYY-MM-DD.md` in the
  space's `timezone` — timestamp, channel, speaker; nothing paraphrased
  on arrival.
- R2.2 Forwarded/external-originated content is wrapped in an
  untrusted-data marker at append time (e.g. `> [!untrusted]`), decided
  by transport metadata, not content heuristics.
- R2.3 Appends are atomic line writes, ordered, and survive a runtime
  restart; a day boundary roll creates the next file.
- R2.4 Leaf appends produce scoped git commits on the vault's commit
  schedule (batched, never per-message fsync storms).

Acceptance: A2.1 a Telegram exchange appears verbatim in today's leaf;
A2.2 a forwarded message carries the marker; A2.3 kill -9 between appends
loses nothing already acked; A2.4 `git log` shows leaf-scoped commits.

## R3 — Nightly rollup (CHG-0206)

- R3.1 A hub-owned per-space job runs at `memory.rollup_time` (default
  04:00 space-local). Go code owns reads, validation, writes, commits,
  retries and limits; the model only returns JSON proposals via the
  space's model-relay.
- R3.2 Steps, in order: (a) day summary + card references + facts with
  owner-quote evidence; (b) append dated facts and propose Compiled
  Truth under the D5 hash check; (c) bilateral card links only when both
  targets exist; (d) bounded CORE.md edit from owner quotations only.
- R3.3 Caps: ≤40 model calls and ≤300k estimated input tokens per night
  (configurable); exceeding caps stops the run cleanly, not mid-write.
- R3.4 An idempotent day cache under `runtime/memory/night/` makes
  restarts resume from the last completed step; a changed already-
  processed entry halts the day and asks the owner to clear the cache.
- R3.5 Fold-up: daily→weekly requires all 7 day states incl. explicit
  no-data days; monthly uses full in-month weeks plus edge days; yearly
  all 12 months.
- R3.6 A completed day records readiness only after its summary commit;
  provider/transport failure marks nothing ready and consumes no
  schema-failure attempt.

Acceptance: A3.1 a seeded day produces summary + ≥1 evidence-linked fact;
A3.2 a mid-run provider kill resumes from the saved step next run;
A3.3 an owner card edit during the run keeps the edit and sets
`truth_pending`; A3.4 a 7-day week (one no-data) yields a weekly summary.

## R4 — Search, link graph and maintenance (CHG-0207)

- R4.1 `memory_search` (hub MCP): BM25 (SQLite FTS5 via
  `modernc.org/sqlite`, or the sanctioned fallback) over
  `cards|summaries|weekly|monthly|yearly|daily` scopes + link-graph
  rerank from `.graph/vault-graph.json` + frontmatter meta in the index.
- R4.2 `## History` sections are excluded unless `history: true`; scope
  values are resolved and confined under the vault root; any engine
  failure degrades to substring scan instead of failing the call.
- R4.3 A deterministic maintenance pass (same schedule family as the
  rollup) rebuilds the graph file, enforces the CORE cap, runs
  description-growth cleanup, commits, and pushes when a remote exists —
  no model calls.
- R4.4 Result rows carry vault-relative paths resolvable by the agent's
  file tools; snippets are bounded.

Acceptance: A4.1 a fact is found by keyword and by graph hop; A4.2 stale
card text does not hit without `history: true`; A4.3 a `../..` scope is
dropped silently; A4.4 graph file regenerates after card edits.

## R5 — Memory write contract and zone guards (CHG-0208)

- R5.1 `memory_card` (hub MCP) ops: `fact` (append-only dated line),
  `truth` (D5 hash-checked Compiled Truth), `merge` (owner instruction
  required at call time — a grant is not an instruction).
- R5.2 Validation errors return the corrected call shape; repeated
  invalid calls hit the existing bounded-failure path.
- R5.3 Generic file-write tools are denied `memory/daily`,
  `memory/summaries`, `memory/cards` via ToolHub path scoping; other
  vault paths (e.g. `library/`) stay writable; `CORE.md` accepts only the
  bounded writer.
- R5.4 Every contract write is a scoped git commit with writer
  attribution (agent / owner / rollup).

Acceptance: A5.1 `fact` appends with date+evidence; A5.2 `merge` without
an owner instruction is refused; A5.3 direct `file_write` into `cards/`
is denied; A5.4 git log attributes each write class.

## R6 — OpenKnowledge service (CHG-0209)

- R6.1 Feature `knowledge_ui` renders one `open-knowledge` compose
  service per space: image built via the hubctl BuildKit path with a
  pinned `@inkeep/open-knowledge` version, volume =
  `spaces/<id>/vault`, `ok init --no-mcp --no-skills` first-boot,
  `replicas: 1`, private network only.
- R6.2 The vault root is the OK project root and contains no secrets by
  layout (D1); an `.okignore`-equivalent excludes `.git`/`.graph`/state.
- R6.3 `OK_BIND=0.0.0.0` + `OK_ALLOW_EXTERNAL=1` +
  `OK_EXTERNAL_URL=http://localhost:<ui_port>`; the port publishes to
  `127.0.0.1` only.
- R6.4 Feature off → no service, no volume mount, no env (deny-wins);
  `doctor` reports the missing pieces when half-configured.

Acceptance: A6.1 `hubctl up` on an enabled space boots OK healthy on
`/readyz`; A6.2 the editor opens via SSH tunnel; A6.3 scaling attempt to
2 replicas is rejected by the render; A6.4 disabled space renders zero
OK resources.

## R7 — OK MCP wiring and capability split (CHG-0210)

- R7.1 `knowledge_ui` publishes the OK HTTP MCP endpoint
  (`http://open-knowledge:8080/mcp`) to the runtime with the read
  surface: `exec`, `search`, `links`, `history`, `audit`, `lint`,
  `config`, `palette`, `preview_url`, `share_link`, `skills` (read ops).
- R7.2 `knowledge_ui_write` adds `write`, `edit`, `delete`, `move`;
  without it those verbs are excluded at the ToolHub/governance layer,
  not merely unadvertised.
- R7.3 The OK tool surface joins the existing projection/allowlist
  machinery (`tools.include`/`tools.exclude`, org `read_only_mcp`); org
  scope may grant `knowledge_ui` read-only to members.
- R7.4 No OK write path may reach outside `spaces/<id>/vault`; contract
  tests prove the boundary.

Acceptance: A7.1 Hermes lists OK read tools under the feature; A7.2 a
`write` call without the write grant is denied with the standard deny
receipt; A7.3 write grant on → attributed edits appear and OK shows them
live; A7.4 tool calls naming paths outside the vault are refused.

## R8 — Human access edge (CHG-0211)

- R8.1 `ui_port` (per-space, `agent.yaml`) renders `127.0.0.1:<ui_port>`
  publish and matching `OK_EXTERNAL_URL`; collisions are diagnosed like
  browser/oauth ports today.
- R8.2 Operations docs cover the SSH-tunnel flow and the
  no-public-exposure default.
- R8.3 The render leaves an explicit hook for the M18 exposure milestone
  (named edge service / host routing) without implementing it.

Acceptance: A8.1 editor reachable through `ssh -L` on the rendered port;
A8.2 second space renders a different port; A8.3 no port is published on
non-loopback interfaces.

## R9 — Migration, eval and hygiene (CHG-0212)

- R9.1 `hubctl memory export-honcho --user <id>` converts a Honcho/native
  memory export into `cards/notes` + `cards/contacts` entries, idempotent
  and dry-run by default.
- R9.2 A Go port of the iva `memory-eval` fixture pattern (seeded vault,
  expected search/rollup outcomes) runs as a repository test — not a live
  model call.
- R9.3 The vault-cleanup analog (collapse duplicated `description`
  growth, cap oversized legacy descriptions, streaming) is available as
  a `hubctl memory vacuum` maintenance command.

Acceptance: A9.1 dry-run reports, apply writes cards; A9.2 eval fixture
passes deterministically; A9.3 vacuum shrinks a seeded bloated vault
without touching facts.

## R10 — Org knowledge brain (CHG-0213, optional stage)

- R10.1 An org scope may render a dedicated `open-knowledge` instance
  rooted at org shared material, granted to members read-only via
  `read_only_mcp`/`tools.include` — write verbs never reach org material
  from a user runtime.

Acceptance: A10.1 member runtime sees org docs via `search`/`exec`;
A10.2 every write verb is denied at governance layer.

## Global invariants

- V1 Nothing under `spaces/<id>` outside `vault/` is reachable by vault
  tools or OK; secrets/env never enter the vault by layout.
- V2 Every mutating capability is a separate grant plus a concrete owner
  instruction at call time (SPEC-0041).
- V3 All memory logic is hub-owned Go (ADR-0003); the model is a
  proposal oracle only; upstream Hermes is unpatched.
- V4 Revocation removes rendered entries on next materialization and
  keeps vault content/history.
- V5 No live-model claims: unit/integration tests use fixture model
  responses; live acceptance is recorded per CHG.
