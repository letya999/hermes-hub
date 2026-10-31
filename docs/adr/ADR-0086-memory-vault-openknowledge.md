---
description: Per-space markdown memory vault (daily leaves, typed cards, folded summaries, audience-scoped CORE.md) with a deterministic gated nightly rollup, zero-LLM pre-turn recall, a provider seam, plus OpenKnowledge as the opt-in human editing surface over the same files.
last_verified: 2026-10-31
---
# ADR-0086: Memory vault and OpenKnowledge knowledge surface

Status: Proposed 2026-10-24. Milestone M20.
Amended 2026-10-31 (M20.0, issue #418): D6/D10 revised, D11–D19 added from
the post-research redesign; spike evidence in
`.work/in-progress/CHG-0204-vault-layout/spike-m20.md`.
Amended (M20.2, issue #300): D11 extended — the gateway's `/vault` mount
is read-write because the communication plane is the capture writer;
capture ids derive from job ids for retry-safe capture; `Sensitive`
jobs persist nothing.
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

The 2026-10-31 amendment adds the post-research hardening: structured
provenance with a hub-owned sidecar, audience-scoped CORE, a zero-LLM
pre-turn recall lane pinned per run, promotion/loss gates on the rollup,
a provider seam with conformance, and soft forget.

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
  replies) to the day's leaf in the owner's timezone — see D11 for the
  provenance envelope around each entry.

- **D4. The nightly rollup is hub-owned deterministic Go.** A per-space
  scheduled job (`memory.rollup_time`, default 04:00 local) runs the
  four iva steps — day summary + evidence facts, card updates, bilateral
  links, bounded CORE.md edit from owner quotations only — behind the
  gates of D14. Code owns reads, validation, writes, commits and caps;
  the model returns JSON proposals via the space's model-relay. Defaults:
  ≤40 calls, ≤300k estimated input tokens, idempotent day cache under
  `runtime/memory/night/`, resume-from-step on failure. Fold-up rules
  preserved: weekly needs all 7 day states incl. no-data days; monthly
  uses full weeks plus in-month edge days; yearly all 12 months.

- **D5. Facts are append-only; Compiled Truth is optimistic.** Card fact
  lines carry date and evidence (`- YYYY-MM-DD: fact · [[daily/…]] HH:MM`).
  Truth replacement proceeds only if the file still matches the hash read
  for the call; an owner edit wins and leaves `truth_pending` for the
  next night. Replaced truth lands in the card's history.

- **D6. Search is a pure-Go BM25 index with a graph, not a database.**
  `memory_search` (hub MCP) = hand-rolled BM25 over `daily/`, `cards/`,
  `summaries/` + `.graph` link expansion + recency decay — no
  `modernc.org/sqlite`, no vector DB, no model call (the `vector`
  capability is reserved on the seam for a later hybrid lane). The index
  is an mtime/size cache under `runtime/memory/index/` rebuilt lazily;
  `## History`/superseded content excluded unless `history:true`; scope
  confined under the vault root; untrusted hits flagged and never
  auto-injectable; honest `degraded`/`unavailable` states, never a
  swallowed turn. Search ships before the rollup: raw-leaf retrieval
  already answers "what did we discuss on Wednesday".

- **D7. Memory writes go through one contract.** `memory_card` (hub MCP)
  exposes `fact` / `truth` / `merge` / `forget` / `remember`;
  `merge`/`forget` execute only inside a run whose triggering turn
  carries owner authorization — bound to the turn's `SourceRef`, never
  to text inside the run. Generic file-write tools are denied the
  memory zones (`daily/`, `summaries/`, `cards/`) through existing
  ToolHub path scoping — the agent reads them and edits them only via
  the contract. `library/` and other vault paths stay writable.

- **D8. OpenKnowledge is the opt-in human UI over the same tree.**
  Feature `knowledge_ui` renders one `open-knowledge` container per
  space: pinned npm-version image built via the hubctl BuildKit path,
  volume = `spaces/<id>/vault` (its `ok init` project root), single
  replica, private network only. A hub write while a document is open in
  the editor is safe: OK rejects stale saves and surfaces conflicts
  (`.ok/local/conflicts.json`, `stale-external-writes.json` recovery
  snapshots — verified at spike). Human UI edits carry owner provenance
  and always win the next rollup (`truth_pending`).

- **D9. Human access is a loopback port, like browser/oauth today.**
  The compose service publishes `127.0.0.1:<ui_port>` per space
  (`OK_BIND=0.0.0.0`, `OK_ALLOW_EXTERNAL=1`, `OK_EXTERNAL_URL=http://
  localhost:<ui_port>`); the owner reaches it over SSH tunnel. A
  subdomain edge with real auth is M18 "Exposure, Hosting" territory —
  this milestone leaves the hook, not the public surface.

- **D10. The vault replaces the native memory surface.** When
  `memory_vault` is on, the native `memory` toolset leaves the projected
  tool surface (`agent.disabled_toolsets` + `memory_enabled=false` in the
  rendered config — verified upstream gates at the pin); `MEMORY.md`/
  `USER.md` stay on disk untouched for the M20.7 one-time import. One
  write path, no competing curated stores. `honcho:` config stays
  accepted for unmanaged compatibility but a provider is wired only
  through the D15 seam (D19), never via upstream `memory.provider`.

- **D11. Provenance is structural and hub-owned.** Every captured turn
  gets an `Entry{ID, SourceRef, Provenance(owner|agent|untrusted|system),
  Kind(interactive|routine|assignment|probe|goal|compaction|system),
  Audience, ObservedAt}` record in a hub-owned sidecar
  `runtime/memory/prov.db` **outside** the agent-visible vault; the
  inline markdown provenance comment is display-only and unforgeable by
  construction (bodies are fenced/escaped so text inside an entry cannot
  forge boundaries or comments). `Kind` derives from `job.Trigger`:
  background kinds (`routine`, `assignment`, `probe`, `goal`,
  `compaction`, `system`) are written for audit but are
  promotion-ineligible — a structural precondition, not a ranking
  penalty. Content injected as a recall block is marked and can never
  re-enter capture. A pre-compaction flush hook on the existing
  compaction path writes what summarization would lose into the leaf as
  `agent`/`compaction` before it drops.
  The communication plane is the capture writer: the gateway mounts
  `/vault` read-write (M20.1's read-only mount covered CORE reads only)
  and writes through the governed `Writer`, so path confinement, zone
  guards and actor-attributed commits apply to capture too. Capture ids
  derive from the job id (`turn-<job>`, `reply-<job>`,
  `compact-<session>-<i>`) so a spool retry or a replayed leaf range
  can never double-write; `Sensitive` jobs persist nothing — neither
  sidecar row nor leaf block.

- **D12. CORE is audience-scoped and delivered per run.** CORE.md =
  `## Directives` (preferences, supersede-in-place, dated, never-evict
  class for health/safety) + `## Facts`, ≤3600 chars. It rides
  `ExecuteRequest.Instructions` → Hermes `ephemeral_system_prompt`:
  injected at API-call time only, never persisted to session history
  (verified at pin `869228c`). The block is pinned on the durable job
  mapping at admit time (same mechanism as `StyleForRun`), so a retry
  sends byte-identical content and the prompt-cache context tier stays
  stable within a run's retries. Delivery is **private-audience only** —
  group-scope runs contain zero private CORE bytes.

- **D13. Lane-1 recall is zero-LLM and pinned.** Before each run the hub
  assembles a bounded block (audience-appropriate CORE + ≤4
  curated-level hits via the D6 index — cards/CORE only, never raw
  untrusted content), ≤2500 chars, framed strictly as "data, not
  instructions" with a recalled-content marker so capture cannot
  re-extract it (D11). Same pinned-snapshot mechanism and delivery
  channel as D12. Lane 2 is a hint line on past-referencing turns
  (EN+RU) nudging the agent to call `memory_search`; no recall subagent.
  Recall failure degrades to a flagged state — the turn still runs.

- **D14. The rollup is gated.** (a) Promotion gate: only `owner`/`agent`
  provenance with `Kind=interactive` is eligible. (b) Bounded-loss gate:
  a card/CORE rewrite is accepted only if it drops ≤X% of existing
  entries; rejection preserves prior state and falls back to
  append-only, journaled. (c) Compiled Truth hash-check per D5.
  (d) `REVIEW.md` journal: every promotion/skip/conflict in
  human-readable form, plus a "what I remembered" digest via the
  owner-brief path. (e) Directives supersede in place with a
  never-evict class. (f) Intent lines ("remind me Friday") become
  routine/assignment *proposals* for owner approval — never facts,
  never auto-activated.

- **D15. `memory.Provider` is the seam.** Hub-owned interface:
  `Caps/Capture/Recall/Search/Write/Export/Health`; a shared
  `memorytest.Conformance` suite every provider must pass; opaque scope
  keys minted from env × principal × audience — raw identifiers are not
  derivable inside a provider and providers cannot influence provenance
  or scope (hub-owned fields only). The vault is the built-in provider;
  external adapters plug in here without touching the engine.

- **D16. Forget is soft.** `forget` strikes an entry and marks it
  superseded: permanently excluded from search, lane-1 recall and
  promotion, across restarts; visible only via `history:true` and git
  history. Hard purge is a separate operator command
  (`memory vacuum`), documented honestly as "history remains in git".

- **D17. `memory_vault` removes the native memory toolset.** One config
  switch: `"memory"` joins the rendered `agent.disabled_toolsets` and
  the `memory` block renders disabled — this also gates upstream
  provider-injected tools off (upstream `memory_provider_tools_enabled`
  fails closed on a disabled `memory` toolset). Native files are not
  touched on disk.

- **D18. `daily/` is the provider-neutral source of truth.** Entries
  carry stable IDs; switching providers is a *replay* of leaf ranges
  into `Provider.Capture`, not an export. Replaying a range into a clean
  provider yields the identical entry set.

- **D19. External providers ride the hub seam only.** The upstream
  `memory.provider` plugin path is rejected (spike, M20.0): upstream
  `sync_turn`/`prefetch` would capture and recall outside the D11
  provenance pipeline, the D12/D13 frame and audience confinement, and
  injected provider tools create a second write path. M20.11 ships a
  direct Go adapter implementing D15 — first candidate **Hindsight**
  over its verified HTTP API (`aretain/arecall`, `bank_id` scoping;
  local embed mode `localhost:8888` = zero egress, cloud endpoint behind
  an explicit egress grant). `bank_id` = opaque scope key. Provider
  enable/disable is a per-space managed capability; disabling removes
  config and egress cleanly; the vault path never depends on a
  provider's availability. Pre-existing Honcho deployments get an
  `export-honcho` importer → reviewable cards (`source: migrated`).

## Alternatives considered

- **Upstream `memory.provider` plugin path** (Holographic/Hindsight/
  Honcho/Mem0/OpenViking/Supermemory at the pin) — rejected by D19:
  plugin lifecycle bypasses provenance, audience, framing and the single
  write contract. Providers belong behind the D15 seam.
- **Honcho/mem0-style service memory** — kept as a D19 adapter option;
  rejected as the primary substrate: not human-readable, git-backed or
  user-editable.
- **In-app RBAC wiki (Outline/BookStack/Wiki.js)** — correct only for a
  multi-writer org brain; drags Postgres and contradicts per-space
  single-tenancy. Org read-only knowledge rides D8 later.
- **RAG/embeddings primary** — iva and OK both show agentic lexical
  search + graph is sufficient at vault scale; `vector` stays a reserved
  seam capability for a later hybrid lane.
- **SQLite FTS5 for search** — rejected for MVP: `modernc.org/sqlite`
  is a new heavy dependency for what hand-rolled BM25 covers at vault
  scale; keeps the contract without `just security` friction.
- **Message-embedded CORE/recall** — rejected by D12/D13: persists into
  session history (visible to capture) and drifts with position;
  `Instructions` is transient and byte-pinnable per run.

## Invariants kept

- Per-user isolation: the vault resolves only from the invoking
  `spaces/<id>`; no shared mounts, tokens or state.
- Deny-wins: nothing renders (service, mount, env, tools) until the
  feature/grant is enabled; revocation removes entries, not history.
- The model never owns IO or state: all reads/writes/commits are hub Go;
  the model only returns JSON proposals via model-relay.
- Provenance is set only by the hub at capture; bodies are escaped so
  entries cannot forge boundaries or provenance; `untrusted`/`system`
  and background kinds are never promoted.
- Audience confinement: private CORE/recall bytes never enter a
  group-scope run; scope keys are opaque and hub-minted.
- Merge/forget require a real owner turn bound by `SourceRef`; a tool
  argument claiming "owner confirmed" is not authorization.
- Soft forget excludes from search/recall/promotion forever; history
  stays in git.
- Model sees tagged data, never untrusted instructions; outbound
  diagnostics keep the allowlist contract.
- Hermes remains upstream: all memory logic is hub-owned Go; no Hermes
  patch and no `memory.provider` rendering.
