---
description: Per-space markdown memory vault (daily leaves, typed cards, folded summaries, audience-scoped CORE.md) with a deterministic gated nightly rollup, zero-LLM pre-turn recall, a provider seam, plus OpenKnowledge as the opt-in human editing surface over the same files.
last_verified: 2026-11-06
---
# ADR-0086: Memory vault and OpenKnowledge knowledge surface

Status: accepted — delivered M20, verified 2026-11-15. Proposed 2026-10-24. Milestone M20.

Amended by [ADR-0095](ADR-0095-toolhub-owned-memory-automation.md): vault ownership and automation moved from runtime to ToolHub (M32).
Amended 2026-10-31 (M20.0, issue #418): D6/D10 revised, D11–D19 added from
the post-research redesign; spike evidence in
`.work/in-progress/CHG-0204-vault-layout/spike-m20.md`.
Amended (M20.2, issue #300): D11 extended — the gateway's `/vault` mount
is read-write because the communication plane is the capture writer;
capture ids derive from job ids for retry-safe capture; `Sensitive`
jobs persist nothing.
Amended (M20.4, issue #419): D13 composition recorded — the ≤2500-char
budget caps the recall block itself (markers + frame + ≤4 card hits +
the lane-2 hint); CORE keeps its own D12 ≤3600-char slice and both ride
the same 4096-byte instructions ceiling with CORE yielding first. A
recall/index failure degrades to a flagged marker-wrapped line and the
run still executes.
Amended (M20.6, issue #303): D7 mechanics recorded — run-scope stamps
carry provenance for owner-turn binding; soft forget lands as
`runtime/memory/forget.jsonl` strikes for entries and `status:
superseded` frontmatter for cards; every mutation and denial journals to
`runtime/memory/writes.jsonl` with the authorizing turn's `SourceRef`;
the `vault:` file root gates non-contract paths behind a stamped private
run with case/fullwidth-folded zone names.
Amended (M32, ADR-0095): the D7 mechanics this paragraph recorded are
superseded — ToolHub owns the vault, the shared
`runtime/run-scope/current` authorization file and the `vault:` file
root are retired, and private-run admission is verified against the
supervisor's durable job record instead of a stamped sidecar.
Amended (M20.7, issue #307): R7 mechanics recorded — native import is a
parse→stage→explicit-apply pipeline: drafts land under
`runtime/memory/import/` (never in searchable card paths), directive-like
sections become review-only candidates that never reach `CORE.md`
automatically, re-import skips existing cards, and card `status:` is
parsed from leading frontmatter only so injected body lines stay inert.
The deterministic eval fixture seeds a real vault through `vault.Daily`,
measures recall@4 ≥ 0.9 on a fresh index, and proves isolation by
building a neighbor index. The live eval questions ride the model relay
with top-4 index evidence as context; reports record per-question
citations and relay errors, land under `.work/tmp` with exclusive-create
names, and a mock relay never counts as live. Vacuum's dry-run is a pure
scan; `--apply` strips struck leaf blocks through the governed writer,
`git rm`s superseded cards, drops prov.db rows and commits — history
keeps everything recoverable.
Amended (M20.8, issue #304): D8 mechanics recorded — the knowledge role
image is a dedicated Dockerfile stage `npm i -g --ignore-scripts
@inkeep/open-knowledge@0.88.2` (exact pin, newest stable ≥7 days old at
selection), built per environment as `hermes-hub:<ver>-<env>-knowledge`
through the hub BuildKit paths (`docker-check`, `docker-pull`, CI image
matrix) with a 600 MB role budget. The service mounts
`spaces/<id>/vault` at `/data` read-write, binds its container interface
via `OK_ALLOW_EXTERNAL=1`, attaches exactly the space-private network
(project default in static mode, the internal agent net in managed
mode), and publishes no host port. Owner provenance is a git-level
sweep: `vault.CommitExternalEdits` commits every uncommitted working-tree
change as `hermes-owner` (excluding `.git/`, `.graph/`, `.ok/` and
`runtime/` sidecars) and runs inside the rollup immediately before the
first governed write, so a rollup commit can never absorb owner bytes
under a system author.
Amended (M20.9, issue #305): R9 mechanics recorded — the read surface
renders as `mcp_servers.open_knowledge` with `tools.include` pinned to
the eight verified read verbs at 0.88.2, on both render paths behind
the `native:knowledge_ui` governance section. Verification corrected
the plan's assumed list: `lint` is fully excluded (its `fix` arg
mutates and include filters names only); `checkpoint`/`share_link`
carry upstream `readOnlyHint` but persist git history / mint publishing
URLs; `preview_url` auto-starts the server. `knowledge_ui_write` is
refused on every tools spelling and tracked as #422 until upstream
gains sub-tree write scoping. The container env gained `PORT=8080`,
`OK_BIND=0.0.0.0` (the upstream allowExternal interlock requires a
non-loopback bind for space-net reachability) and `HOME=/tmp` (image
HOME is on the read-only rootfs); `skip_preflight` keeps a down OK
from stalling the runtime.
Amended (M20.10, issue #306): R10 mechanics recorded — `ui_port` in
`agent.yaml` renders the single publish `<ui_bind>:<ui_port>:8080` plus
`OK_EXTERNAL_URL=http://localhost:<ui_port>`; `ui_bind` defaults to
`127.0.0.1`, accepts `::1` (rendered `[::1]:<port>:8080`) and rejects
anything wider as a hard `Validate` error. `ui_port` validates like the
other host ports (1024–65535, no collision with browser/oauth/slack
events) and refuses to render without `knowledge_ui`. R10.2's "hubctl
status" is `hubctl doctor` — there is no separate status command;
`KnowledgeDoctor` prints the edge, the `ssh -L` hint and a real TCP
reachability probe (occupied port = listening; the probe cannot tell the
editor from a squatter, but a true conflict already fails at compose
publish). `ui_expose` is the reserved M18 key — set today it fails
honestly at `Validate`; the M18 edge service decides the one-time `?t=`
link auth/expiry contract there. Carry fix: `native:knowledge_ui` was
missing from `SeedInventory` — a real space's wish denied as "section
not in the tool inventory" and could never be granted or
migration-preserved; it is now seeded deny-default like `memory_vault`.
Amended (M20.11, issue #330): R11 mechanics recorded — the first
adapter is `internal/memory/providers/hindsight`, a stdlib HTTP client
over the verified wire API at pin `869228c`: retain upserts by
`document_id` = entry id (capture idempotence), documents list+detail
paged export restoring `original_text` plus hub metadata
(`hub_provenance`/`hub_kind`/`hub_audience`/`hub_source_ref`/
`hub_entry_id`), recall→Search hits, `/health`→Health; `Recall` and
`Write` report `ErrUnsupported` — reflect is not evidence and external
Hindsight cannot honor governed writes. `bank_id` is the opaque scope
key verbatim; the adapter rejects traversal-shaped keys regardless of
caller. The capability is `tools: {memory_provider: {via: native}}`
behind `native:memory_provider` governance, fields `memory_provider`
(kind) + `memory_provider_url` (absolute http(s)) +
`memory_provider_token_env` (secrets *name* — literals refuse) in
`agent.yaml`; render writes `vault/runtime/memory/provider.json`,
disabling removes it, and managed mode allowlists the endpoint host
while the capability stands. `hubctl memory replay` streams `prov.db`
append-order into `Provider.Capture`, minting each bank id from
`runtime/memory/scope.key` (hub-generated, 0600) over
env×principal×audience, checkpointing `runtime/memory/replay.cursor`
per entry so interruption resumes and re-runs capture nothing.
`hubctl memory export-honcho --file` parses the verified upstream
conclusions export (Page `items` or the piped array) into reviewable
`source: migrated` card drafts through the M20.7 import lane — staged,
never silently applied. Live protocol acceptance (a real Hindsight
deployment, flush-on-stop, revocation timing) stays recorded pending —
fixture tests verify the contract, never deployment.
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

  2026-10-31 amendment (M20.3): the query's audience travels through a
  hub-stamped sidecar, never through tool arguments. The gateway worker
  writes `runtime/run-scope/current` — `{job_id → {audience, expires}}`
  — before dispatch and clears it on return; expiry is the run budget,
  so a crash-orphaned stamp cannot outlive its run. The vault mount is
  read-write to the runtime (the agent edits memory only through the
  governed writer — a forged stamp still cannot lift the confinement,
  since the reader takes the most restrictive live audience: any active
  group run confines every in-flight call to "group"), and an absent or
  expired stamp fails closed to "group". Persisted index snapshots live under
  `runtime/memory/index/` inside the vault root like `prov.db`; a
  read-only or missing mount degrades persistence to the in-process
  cache without failing queries.

- **D7. Memory writes go through one contract.** `memory_card` (hub MCP)
  exposes `fact` / `truth` / `merge` / `forget` / `remember`;
  `merge`/`forget` execute only inside a run whose triggering turn
  carries owner authorization — bound to the turn's `SourceRef`, never
  to text inside the run. Generic file-write tools are denied the
  memory zones (`daily/`, `summaries/`, `cards/`) through existing
  ToolHub path scoping — the agent reads them and edits them only via
  the contract. `library/` and other vault paths stay writable.

  2026-10-31 amendment (M20.6): the turn's authorization is the live
  run-scope stamp, which now carries `provenance` beside `audience` —
  `merge`/`forget` execute only when a live stamp holds owner
  provenance, every live stamp must be private-audience (a concurrent
  group run confines writes exactly like reads), and expired or
  argument-claimed authorization is denied. The authorizing stamp's job
  id is the `SourceRef` journaled to `runtime/memory/writes.jsonl` for
  every mutation and every denial. Soft forget is two hub-owned marks:
  `runtime/memory/forget.jsonl` strikes daily-leaf entry ids and a
  governed `status: superseded` frontmatter write strikes cards — the
  indexer sets `Doc.Superseded` from both, so struck records leave
  search, lane-1 recall and promotion permanently and stay visible only
  through `history:true` and git. The `vault:` file root gives the
  agent `library/` and ordinary vault paths under a stamped private run
  while denying `memory/`, `runtime/` and dot paths; zone names compare
  with case and fullwidth folding so `MEMORY/`, `ｍｅｍｏｒｙ/` and
  dot-segment tricks cannot alias the guard.

- **D8. OpenKnowledge is the opt-in human UI over the same tree.**
  Feature `knowledge_ui` renders one `open-knowledge` container per
  space: pinned npm-version image built via the hubctl BuildKit path,
  volume = `spaces/<id>/vault` (its `ok init` project root), single
  replica, private network only. A hub write while a document is open in
  the editor is safe: OK rejects stale saves and surfaces conflicts
  (`.ok/local/conflicts.json`, `stale-external-writes.json` recovery
  snapshots — verified at spike). Human UI edits carry owner provenance
  and always win the next rollup (`truth_pending`).

  2026-11-01 amendment (M20.9): the container also serves the runtime a
  read-only MCP surface — `mcp_servers.open_knowledge` →
  `http://open-knowledge:8080/mcp` with `tools.include` pinned to the
  eight verified read verbs at 0.88.2 (`audit`, `config`, `exec`,
  `history`, `links`, `palette`, `search`, `skills`), identical on the
  unmanaged and managed renders behind the `native:knowledge_ui`
  section. The include list is the enforcement — mutation verbs never
  register. Three verification corrections to the plan's assumed list:
  `lint` is excluded entirely (its `fix` arg mutates and include
  filters names, not args); `checkpoint` and `share_link` carry
  upstream `readOnlyHint` yet persist git history / mint a publishing
  URL; `preview_url` auto-starts the server on a cold call. `exec` is
  admitted — upstream scopes it to a read-only shell allowlist inside
  the container. `knowledge_ui_write` remains deferred (#422): upstream
  has no sub-tree write scoping, so `memory_card` stays the only agent
  write path. `skip_preflight` renders so a down OK fails calls
  honestly without stalling the runtime. The service env gained
  `PORT=8080` (dynamic otherwise), `OK_BIND=0.0.0.0` (the allowExternal
  interlock requires it for non-loopback) and `HOME=/tmp` (the image
  HOME sits on the read-only rootfs).

  2026-11-06 amendment (M32, issue #520): D8's service render and the
  M20.9 read surface are superseded — ADR-0095 D2 makes ToolHub the
  vault's sole owner, and no container but ToolHub may mount it. The
  `open-knowledge` compose service, its `vault → /data` bind and the
  `mcp_servers.open_knowledge` runtime route are all retired on both
  renders; `HUB_MANAGED_KNOWLEDGE_UI`/`MaterializeOptions.KnowledgeUI`
  leave the attestation vocabulary. Upstream OK cannot act as a
  governed-API client, so `knowledge_ui` stays a valid feature that
  renders nothing — explicitly unavailable in `hubctl doctor` and
  docs rather than partially open; a stale `stack.yaml` declaration
  fails the designed declared-but-unrendered check until republished.
  Vault content, `.ok/` state and git history are never deleted.
  Knowledge edits for the model ride the ordinary confirmed writer
  (`memory_card op=propose` + the owner's verified confirmation);
  `knowledge_ui_write` stays deferred under #422 until an adapted
  governed knowledge surface exists. The `native:knowledge_ui`
  inventory entry and the `knowledge-ui`/`ui_port`/`ui_bind`/
  `ui_expose` config vocabulary stay accepted — inert, not removed.

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

  2026-10-31 amendment (M20.5): the fired rollup folds the **last
  closed local day** — a 04:00 tick on day D rolls up leaf D-1, so every
  calendar leaf is complete when promoted and each is processed exactly
  once; the last-run marker pins the run day, keeping once-per-day
  firing. A day whose leaf has no eligible entries completes its day
  state with zero model calls and writes no summary file — folds render
  it as the explicit "(no data)" marker. Fold-up is a sweep inside the
  same tick: after the day run succeeds, the job attempts the most
  recently closed week/month/year whose summary file is absent; each
  fold self-gates on terminal inputs, written summaries are immutable
  (owner edits never clobbered, no re-firing), fold calls share the
  night's spend cap, and a fold error stops the sweep for the night —
  the next tick retries it. Verified intents land as draft assignments
  in the owner's own envelope via a deterministic id, so a rollup retry
  replays the stored record instead of duplicating it.

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

- **D20. Leaves are a bounded tape, not an archive.** (CHG-0278) Daily
  leaves exist to feed the rollup and to audit; once a day's
  distillation is *provably* done they stop earning their bytes. Two
  deterministic night lanes run after `night.Run` + folds, both off by
  default: FOLD stubs non-promotion-eligible block bodies in an
  oversized completed leaf (mem:id headers and interactive owner/agent
  blocks stay verbatim; a verify pass must confirm id counts and
  eligible bodies or nothing commits; owner edits win the WantSHA
  check) — and the same proposal call is asked for `long_summary` so
  detail lands in `summaries/daily/` before bytes are cut. EXPIRE
  `git rm`s a leaf older than `memory_leaf_retention_days` and drops its
  prov.db rows — only when day-state is complete AND the daily summary
  exists; unverified days are kept and reported, never deleted. Every
  action journals to `REVIEW.md` and merges into the morning digest:
  housekeeping is never silent. Git history still holds expired bytes —
  true erasure needs a separate history-rewrite policy, documented not
  hidden.

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
