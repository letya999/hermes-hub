# M20 research — memory vault and OpenKnowledge

Evidence collected 2026-10-24. Two upstream sources studied at depth.

## smixs/iva-agent — the memory design to port

MIT, TypeScript on Node 24, Telegram assistant whose memory is a plain
markdown Obsidian-compatible vault (separate git repo). Sources:
`docs/memory.md`, `agent/lib/{vault-*,card-*,memory-night-*}`, 
`agent/tools/{memory_search,write_card}.ts`, `agent/schedules/memory-night.ts`,
`vault-template/`, `scripts/memory/{night,brain}.ts`, `scripts/fixtures/memory-eval.json`.

What actually makes it work (port all of it):

- **Layered tree**: `daily/YYYY-MM-DD.md` verbatim leaves (owner turns +
  agent replies, nothing paraphrased on arrival); typed cards
  `{contacts,projects,decisions,ideas,notes}/`; `summaries/` folded
  day→week→month→year; `CORE.md` ≤3600 chars in every prompt;
  `PERSONA.md` (our `SOUL.md` analog); `schema.json`; `.graph/vault-graph.json`.
- **Nightly rollup** (`memory-night`, 04:00 local): code owns all IO;
  model returns lenient-parsed JSON proposals only. Four steps:
  1) day summary + card refs + evidence-backed facts;
  2) append dated facts + propose whole new Compiled Truth;
  3) bilateral card links only when both targets exist;
  4) bounded CORE.md edit from owner quotations only.
  Caps 40 calls / 300k est input tokens; usage gate before each call.
- **Append-only facts**: `- YYYY-MM-DD: fact · [[daily/YYYY-MM-DD]] HH:MM`,
  evidence must be an owner quote.
- **Optimistic Compiled Truth**: model returns the whole new truth; it is
  applied only if the file still matches the hash read for the call;
  owner edit wins → `truth_pending` retried next night; replaced truth
  goes to the card's `## History`.
- **Idempotent day cache** `data/memory/night/`: resume-from-step after
  provider failure; a changed already-processed entry halts and asks the
  owner to clear the cache — no silent reprocessing.
- **Fold-up semantics**: weekly requires all 7 day states incl. explicit
  no-data days; monthly = full in-month weeks + edge-week in-month days;
  yearly = 12 months.
- **`write_card` contract**: ops `fact`/`truth`/`merge`; `merge` requires
  owner confirmation; schema errors return the corrected call shape;
  repeated-failure limit enforced. Generic `write_file` is forbidden on
  `daily|summaries|cards` — only `library/` etc. stay writable.
- **`memory_search`**: node:sqlite FTS5 BM25 + link-graph rerank from a
  nightly `.graph` rebuild; frontmatter meta indexed; `## History`
  excluded unless asked; model-supplied scope dirs resolved and confined
  under vault root (traversal dropped silently); per-file mtime+size
  cache; soft degrade to substring scan — the turn never fails; RU
  stemming + ё→е normalization.
- **`brain` maintenance** (05:00): deterministic, no model — commits
  owner edits, rebuilds graph, checks CORE cap, pushes if remote.
- **Git policy**: vault is its own repo; scoped commits per writer
  class; `.gitignore`-excluded files stay on disk and are logged as
  outside-backup rather than blocking the night.
- **Security gates**: forwarded/external content passes an injection
  screen and reaches the model tagged as data; outbound path has a
  secret-redaction gate; allowlist fails closed.
- **Eval**: `scripts/fixtures/memory-eval.json` — seeded-vault eval
  fixture pattern worth porting to Go testdata.

Non-memory features noted but out of M20 scope: userbot anti-ban
guardrail (relevant to `telegram_user` later), quiet update checks,
token accounting (`/usage`).

## inkeep/open-knowledge — the UI/MCP surface

GPL-3.0, ~4.7k★, TS/Node 24 pnpm monorepo (`app/cli/core/server/desktop/
plugin/native-config`). Sources: README, `docs/content/reference/mcp.mdx`,
`remote-control/methods/{docker,cli}.mdx`, `authentication.mdx`,
`integrations/hermes.mdx`, `agentic-search.mdx`, `what-open-knowledge-writes.mdx`.

- 19-tool MCP server over a markdown project: `exec` (read-only shell
  with frontmatter+backlinks+history+comments attached), `search`
  (BM25+title+recency, optional embeddings opt-in), `links`, `lint`,
  `audit`, `history`/`restore_version` (shadow git), `skills` (skills.sh),
  `config`, `palette`, `preview_url`, `share_link`, and write verbs
  `write`/`edit`/`delete`/`move` routed through the collab server.
- Hermes is a documented first-class harness: `ok init` writes
  `mcp_servers` into `~/.hermes/config.yaml`.
- Docker: no official image — documented recipe `node:24-slim` +
  `npm i -g @inkeep/open-knowledge@<ver>` + volume `/data` +
  `ok init --no-mcp --no-skills` first boot + `ok start`. Hard rules:
  single-writer per project volume (1 replica, never scale), persistent
  volume with real fs, WS+SSE ingress, `OK_ALLOW_EXTERNAL=1` +
  `OK_EXTERNAL_URL` for non-loopback bind, `/healthz` + `/readyz`.
- Auth: none built in; docs prescribe an edge (bearer on `/mcp`, cookie
  login for the editor). On a private compose network the trust model is
  the same as hub's existing services; human access via loopback publish
  + SSH tunnel fits the current browser/oauth-port posture. Subdomain
  edge = M18 scope.
- Read/write tool split maps onto the capability model exactly:
  `knowledge_ui` = read surface, `knowledge_ui_write` = the 4 verbs.
- Concurrent-edit protection exists for MCP `replace`; our rollup writes
  files directly — OK picks changes up via its watcher (single-writer is
  about server processes, not the FS).

## Local anchors found

- `internal/stack/config.go` `Features` registry + `agent.yaml` `memory:`/
  `honcho` → `writeHonchoConfig`; `render.go` composes `platform_toolsets`
  and `memory.memory_enabled`/`user_profile_enabled`.
- ADR-0050: `<state>/knowledge/<principal>` is operator-staged sources —
  different concern; vault dir named `vault/` to avoid collision.
- `internal/agenttools` = hub MCP surface for workspace/doc/image tools —
  home for `memory_search`/`memory_card`.
- `internal/communication` = gateway seeing every message — owner of leaf
  ingest; `internal/supervisor`/`internal/routines` = hub-owned scheduling.
- `model-relay` + `serviceauth` = per-space model calls for the rollup.
- `google/jsonschema-go` already a dependency → card schema validation.
- `modernc.org/sqlite` NOT yet a dep → needs `just security` for FTS5;
  fallback pure-Go BM25 keeps the contract.
- Existing ports model (oauth_port, browser_port per space) → `ui_port`
  for OK loopback publish.
