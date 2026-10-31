---
description: Per-space markdown memory vault (daily leaves, typed cards, folded summaries, CORE.md) with a deterministic nightly rollup, plus OpenKnowledge as the opt-in human/agent editing surface over the same files.
last_verified: 2026-10-24
---
# ADR-0086: Memory vault and OpenKnowledge knowledge surface

Status: Proposed 2026-10-24. Milestone M20.
Requirements: [SPEC-0085](../../specs/active/SPEC-0085-memory-vault-openknowledge.md).
Research: `.work/in-progress/CHG-0204-vault-layout/research.md`.

## Context

Memory today is the upstream Hermes `memory` toolset (`agent.yaml` `memory:`)
plus an optional Honcho backend (`honcho.json`). Both are session/working
memory: nothing distills months of owner interaction into durable,
human-readable knowledge, and the owner has no UI over what the agent
remembers. Two external projects define the target shape:

- **iva-agent** (smixs/iva-agent, MIT): a layered markdown vault —
  `daily/` verbatim leaves, typed `cards/`, `summaries/` folded day→week→
  month→year, a capped `CORE.md` riding every prompt, a deterministic
  nightly rollup where code owns IO and the model only proposes JSON,
  BM25+link-graph search, append-only evidence-linked facts, and an
  optimistic-hash "Compiled Truth" so owner edits always win.
- **OpenKnowledge** (inkeep/open-knowledge, GPL-3.0): a WYSIWYG markdown
  IDE whose MCP server (19 tools) is built for agents — enriched reads,
  BM25 search, link graph, lint/audit, git history — with a documented
  Hermes integration and a documented Docker deployment (single-writer
  per project, no built-in auth, edge-proxy model).

## Decisions

- **D1. One markdown tree is the memory substrate.** `spaces/<id>/vault/`
  is a standalone git repository holding `memory/` (vault layers below)
  and free user content. It is mounted into the runtime at `/vault` and
  into the knowledge UI. The name `vault/` avoids collision with
  `<state>/knowledge/<principal>` from ADR-0050 (operator-staged sources).
  Secrets, `runtime/`, `broker/` and `*.env` never live under it — the
  boundary is structural, not a filter.

- **D2. Layers are iva's tree, in our layout.** `memory/daily/YYYY-MM-DD.md`
  (verbatim leaves), `memory/cards/{contacts,projects,decisions,ideas,
  notes}/` (typed, `schema.json`-validated), `memory/summaries/{daily,
  weekly,monthly,yearly}/`, `memory/CORE.md` (≤3600 chars, always in
  prompt context), `memory/CORE.history.md`, `attachments/`. The vault
  contains data only; all logic is hub Go code.

- **D3. Leaf ingest is a control-side append, not an agent skill.** The
  communication plane appends verbatim entries (owner turns and agent
  replies) to the day's leaf in the owner's timezone. Forwarded/external
  content is wrapped in an untrusted-data marker at write time — the
  screen is mechanical, not a prompt instruction.

- **D4. The nightly rollup is hub-owned deterministic Go.** A per-space
  scheduled job (`memory.rollup_time`, default 04:00 local) runs the
  four iva steps — day summary + evidence facts, card updates, bilateral
  links, bounded CORE.md edit from owner quotations only. Code owns
  reads, validation, writes, commits and caps; the model returns JSON
  proposals via the space's model-relay. Defaults: ≤40 calls, ≤300k
  estimated input tokens, idempotent day cache under
  `runtime/memory/night/`, resume-from-step on failure. Fold-up rules
  preserved: weekly needs all 7 day states incl. no-data days; monthly
  uses full weeks plus in-month edge days; yearly all 12 months.

- **D5. Facts are append-only; Compiled Truth is optimistic.** Card fact
  lines carry date and evidence (`- YYYY-MM-DD: fact · [[daily/…]] HH:MM`).
  Truth replacement proceeds only if the file still matches the hash read
  for the call; an owner edit wins and leaves `truth_pending` for the
  next night. Replaced truth lands in the card's history.

- **D6. Search is an agent tool with a graph, not grep.** `memory_search`
  (hub MCP) = SQLite FTS5 BM25 + link-graph rerank over a nightly-rebuilt
  `.graph/vault-graph.json` + frontmatter meta; `## History` sections
  excluded unless asked; scope paths confined under the vault root; soft
  degrade to substring scan on any engine failure. `modernc.org/sqlite`
  is the sanctioned FTS driver (pure Go, no cgo) pending `just security`;
  if rejected, a pure-Go BM25 index keeps the contract.

- **D7. Memory writes go through one contract.** `memory_card` (hub MCP)
  exposes `fact` / `truth` / `merge`; `merge` requires a concrete owner
  instruction at call time. Generic file-write tools are denied the
  memory zones (`daily/`, `summaries/`, `cards/`) through existing
  ToolHub path scoping — the agent reads them and edits them only via
  the contract. `library/` and other vault paths stay writable.

- **D8. OpenKnowledge is the opt-in UI and agent-MCP surface over the
  same tree.** Feature `knowledge_ui` renders one `open-knowledge`
  container per space: pinned npm-version image built via the hubctl
  BuildKit path, volume = `spaces/<id>/vault` (its `ok init` project
  root), single replica, private network only. Hermes reaches it as an
  MCP server at `http://open-knowledge:8080/mcp` — read tools under
  `knowledge_ui`, the `write/edit/delete/move` verbs under a separate
  `knowledge_ui_write` grant, matching SPEC-0041 deny-wins and the
  SPEC-0040 mutation-grant pattern.

- **D9. Human access is a loopback port, like browser/oauth today.**
  The compose service publishes `127.0.0.1:<ui_port>` per space
  (`OK_BIND=0.0.0.0`, `OK_ALLOW_EXTERNAL=1`, `OK_EXTERNAL_URL=http://
  localhost:<ui_port>`); the owner reaches it over SSH tunnel. A
  subdomain edge with real auth is M18 "Exposure, Hosting" territory —
  this milestone leaves the hook, not the public surface.

- **D10. Native memory and Honcho stay; the vault is the long-term
  layer.** `memory` toolset remains working/session memory; `honcho`
  remains a supported config for existing deployments. Migration is an
  export-to-cards tool, not a flag day.

## Alternatives considered

- **Honcho/mem0-style service memory** — kept as option; rejected as the
  primary because it is not a human-readable, git-backed, user-editable
  substrate and adds a network service without adding an owner UI.
- **In-app RBAC wiki (Outline/BookStack/Wiki.js)** — correct only for a
  multi-writer org brain; drags Postgres and contradicts per-space
  single-tenancy. Org read-only knowledge rides D8 later.
- **RAG/embeddings primary** — iva and OK both show agentic lexical
  search + graph is sufficient at vault scale; embeddings stay an
  optional OK/rollup setting with content-egress consent.

## Invariants kept

- Per-user isolation: the vault resolves only from the invoking
  `spaces/<id>`; no shared mounts, tokens or state.
- Deny-wins: nothing renders (service, mount, env, tools) until the
  feature/grant is enabled; revocation removes entries, not history.
- Model sees tagged data, never untrusted instructions; outbound
  diagnostics keep the allowlist contract.
- Hermes remains upstream: all memory logic is hub-owned Go; no Hermes
  patch.
