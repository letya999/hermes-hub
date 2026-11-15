---
description: Frozen requirements for the M20 memory vault (provenance-aware leaves, provider seam, audience-scoped CORE, pre-turn recall, gated rollup, search, write contract, soft forget) and the opt-in OpenKnowledge UI surface.
last_verified: 2026-11-08
---
# SPEC-0085: Memory vault and OpenKnowledge knowledge surface

Frozen: 2026-10-24. Re-frozen: 2026-10-31 (M20.0, issue #418) — new stage
map and requirement set per amended ADR-0086 D1–D19; spike evidence in
`.work/in-progress/CHG-0204-vault-layout/spike-m20.md`.
Research: `.work/in-progress/CHG-0204-vault-layout/research.md`.
Ported-from reference: smixs/iva-agent `docs/memory.md`,
`agent/lib/{vault,card,memory-night}*`, `agent/tools/{memory_search,write_card}.ts`.

Stage map: R1=M20.1/#299, R2=M20.2/#300, R3=M20.3/#302, R4=M20.4/#419,
R5=M20.5/#301, R6=M20.6/#303, R7=M20.7/#307, R8=M20.8/#304,
R9=M20.9/#305, R10=M20.10/#306, R11=M20.11/#330.

## R1 — Vault substrate, provider seam, audience-scoped CORE (CHG-0204)

- R1.1 `memory_vault` is an opt-in user-scope feature. When enabled,
  `hubctl render` creates `spaces/<id>/vault/` as a git repository with
  the D2 layout, mounts it into the runtime at `/vault`, and renders no
  other effect; scaffold is idempotent and never overwrites. A disabled
  or absent feature renders no mount, env, prompt fragment or job
  (deny-wins); org scope is rejected.
- R1.2 `schema.json` validates card frontmatter (type, status,
  confidence, source, tags, aliases, created/updated) via
  `google/jsonschema-go`; invalid cards are reported with field-level
  errors, never silently repaired.
- R1.3 `internal/memory` defines the `Provider` contract
  (`Caps/Capture/Recall/Search/Write/Export/Health`), the shared
  `memorytest.Conformance` suite, and opaque scope minting
  (env × principal × audience): scope keys differ across dev/prod, users
  and audiences; raw identifiers are not derivable inside a provider;
  provenance and scope are hub-owned fields only.
- R1.4 A single `VaultWriter` owns vault writes: per-file content-hash
  check, authored scoped git commits; a stale-hash write is rejected; an
  owner-edit conflict preserves the owner version and sets
  `truth_pending`.
- R1.5 `CORE.md` = `## Directives` + `## Facts`, hard-capped at 3600
  chars, re-read at each use — never a stale snapshot. It is delivered
  per run via `ExecuteRequest.Instructions` to **private-audience runs
  only**; a group-scope run contains zero private CORE bytes.
- R1.6 With `memory_vault` on, the native `memory` toolset is absent
  from the projected tool surface (`agent.disabled_toolsets` +
  `memory_enabled=false`); `MEMORY.md`/`USER.md` stay untouched on disk.
- R1.7 `memory.daily` is the provider-neutral source of truth: entries
  carry stable IDs; provider migration is a replay of leaf ranges.

Acceptance: A1.1 fresh init → template tree + initial commit, re-init
no-op; A1.2 CORE in effective run context for private runs only (direct
DM-vs-group test); A1.3 `memorytest.Conformance` green on the vault
provider and an in-memory fake; A1.4 scope isolation per R1.3; A1.5
stale-hash write rejected, owner conflict → `truth_pending`; A1.6 native
`memory` tool absent, files untouched; A1.7 feature off → zero vault
effects.

## R2 — Provenance-aware daily capture (CHG-0205)

- R2.1 The communication plane appends every accepted owner turn and
  agent reply verbatim to `vault/memory/daily/YYYY-MM-DD.md` in the
  space's `timezone`: timestamp, speaker, channel, audience; nothing
  paraphrased on arrival. Appends are atomic, ordered, persist-before-ack
  (a mid-append `kill -9` loses nothing acknowledged), and roll at
  owner-local midnight, DST-safe.
- R2.2 Each entry gets a hub-owned `Entry{ID, SourceRef, Provenance,
  Kind, Audience, ObservedAt}` in the `runtime/memory/prov.db` sidecar
  (outside the vault); the inline markdown comment is display-only.
  Bodies are fenced/escaped: text inside an entry cannot forge entry
  boundaries or provenance comments.
- R2.3 Forwarded/external-originated content is `untrusted` **at the
  entry level** from transport metadata, not content heuristics;
  "owner: remember X" inside untrusted text stays untrusted.
- R2.4 `Kind` derives from `job.Trigger`; `routine|assignment|probe|
  goal|compaction|system` entries are written for audit but are
  promotion-ineligible.
- R2.5 Recalled content shown to the agent is marked and excluded from
  capture; it can never re-enter a leaf as new content.
- R2.6 A pre-compaction flush hook writes turn content that
  summarization would drop into the leaf as `agent`/`compaction` before
  it is lost.
- R2.7 Replaying a leaf range into a clean provider yields the identical
  entry set (stable IDs; migration = replay).

Acceptance: A2.1 verbatim exchange lands with full provenance; A2.2
persist-before-ack survives kill -9 and restart; A2.3 untrusted at
entry level; A2.4 100 background wakes → 0 promotion-eligible; A2.5
recalled block never re-enters; A2.6 DST-safe midnight boundary; A2.7
replay determinism.

## R3 — `memory_search` BM25 + link graph (CHG-0207)

- R3.1 `memory_search` (hub MCP): pure-Go BM25 (unicode tokenization,
  RU/EN without a stemmer dependency) over `daily/` + `cards/` +
  `summaries/` + `.graph` link expansion + recency decay. No model call,
  no new dependency, no vector lane.
- R3.2 The index is an mtime/size cache under `runtime/memory/index/`
  (outside the vault), rebuilt lazily per scope; it self-heals on
  writes; index files carry no raw identifiers.
- R3.3 Every hit carries an evidence reference (file + date + line
  range); no orphan text.
- R3.4 Filters: audience confinement (group scope never sees private
  hits; dev/prod and cross-space isolation), superseded/forgotten
  excluded unless `history:true`, untrusted hits flagged and
  structurally excluded from the R4 auto-recall path.
- R3.5 Tool `memory_search(query, limit, from, to, kinds)` reports
  distinct empty / partial / unavailable / degraded states; a degraded
  or stale index never swallows the turn.
- R3.6 Maintenance: `hubctl memory search --reindex`; doctor reports a
  stale index. p95 < 50 ms on a 10k-entry fixture.

Acceptance: A3.1 raw-leaf search finds the right day before any rollup;
A3.2 evidence refs on every hit; A3.3 history/forget semantics; A3.4
scope isolation; A3.5 degraded reporting; A3.6 perf bound.

## R4 — Pre-turn recall lane (CHG-0276)

- R4.1 Before each run the hub assembles a deterministic block:
  audience-appropriate CORE + ≤4 curated-level hits (cards/CORE only —
  never raw untrusted content), recency-decayed ordering, hard budget
  ≤2500 chars, framed as "data, not instructions".
- R4.2 The block is computed once per job at admit time and pinned on
  the durable mapping; a retry sends byte-identical recall bytes.
- R4.3 Delivery rides `ExecuteRequest.Instructions` (ephemeral at
  API-call time, never persisted — ADR D12); zero model calls on the
  recall path; p95 < 50 ms; failure degrades to a flagged state and the
  turn still runs.
- R4.4 The block carries the recalled-content marker so R2 capture
  cannot re-extract it; a stored fact containing directives stays inert.
- R4.5 Audience gate: private scope → CORE + hits; group/other → only
  that audience's slice, never private bytes.
- R4.6 Lane 2: a single hint line on past-referencing turns (EN+RU
  trigger phrases) nudging `memory_search`; nowhere else.

Acceptance: A4.1 yesterday's fact appears in today's run with no agent
action, across restart/new session; A4.2 byte-identical retry; A4.3
group run = zero private bytes; A4.4 recalled block non-extractable;
A4.5 directive-laden hit inert; A4.6 degraded still runs; A4.7 hint only
on past-referencing turns.

## R5 — Night rollup: gates, directives, journal (CHG-0206)

- R5.1 Hub-owned per-space job at `memory.rollup_time` (default 04:00
  space-local). Go owns reads, validation, writes, commits, retries,
  limits; the model returns JSON proposals via the space's model-relay.
- R5.2 Steps in order: (a) day summary + card references + facts with
  owner-quote evidence; (b) append dated facts + propose Compiled Truth
  under the D5 hash check; (c) bilateral card links only when both
  targets exist; (d) bounded CORE.md edit from owner quotations only.
- R5.3 Promotion gate: only `owner`/`agent` provenance with
  `Kind=interactive` is eligible; `untrusted`/`system` and background
  kinds can never be promoted on any path.
- R5.4 Bounded-loss gate: a card/CORE rewrite is accepted only if it
  drops ≤X% of existing entries; rejection preserves prior state, falls
  back to append-only, and is journaled.
- R5.5 CORE `## Directives` supersede in place (dated) — preferences
  replace, they do not accumulate; never-evict class for health/safety.
- R5.6 `REVIEW.md` journals every promotion/skip/conflict; a "what I
  remembered" digest rides the existing owner-brief path.
- R5.7 Intent lines become routine/assignment proposals for owner
  approval — never stored facts, never auto-activated.
- R5.8 Caps ≤40 model calls / ≤300k estimated input tokens per night
  (configurable); exceeding stops cleanly, not mid-write. Idempotent
  day cache under `runtime/memory/night/` resumes from the last
  completed step; a changed already-processed entry halts the day.
- R5.9 Fold-up: daily→weekly needs all 7 day states incl. no-data;
  monthly = full in-month weeks + edge days; yearly = 12 months.
- R5.10 Oversized eligible days (above `memory_leaf_fold_bytes`) ask the
  same proposal call for `long_summary`; the detailed digest lands in
  `summaries/daily/<day>.md` so detail survives the leaf fold — no
  second model call.

Acceptance: A5.1 seeded day → summary + evidence-linked fact; mutated
quote rejected; A5.2 hash mismatch keeps owner edit + `truth_pending`;
A5.3 directive replaced in place; A5.4 crafted lossy rewrite rejected,
prior state preserved, journaled; A5.5 zero promotions from
untrusted/background; A5.6 kill mid-step resumes; same-day second run
no-ops; A5.7 caps enforced; A5.8 morning digest; A5.9 intent → proposal;
A5.10 big day → long_summary persisted as the daily summary.

## R6 — `memory_card` contract, soft forget, zone guards (CHG-0208)

- R6.1 `memory_card` (hub MCP) ops: `fact`, `truth`, `merge`, `forget`,
  `remember`. `remember` lands in `cards/notes/inbox` and is searchable
  the same day (index refresh on write).
- R6.2 `merge`/`forget` execute only inside a run whose triggering turn
  carries owner authorization — bound to the turn's `SourceRef`; a tool
  argument claiming "owner confirmed" is denied; a replayed old
  authorization is denied.
- R6.3 Soft forget: the entry is struck, marked superseded, and
  permanently excluded from search, lane-1 recall and promotion across
  restarts; visible only via `history:true` and git. Hard purge is
  `memory vacuum` (R7).
- R6.4 Generic file-write tools are denied `memory/daily`,
  `memory/summaries`, `memory/cards` via ToolHub path scoping on every
  write surface (file tools, `memory_card`, OK MCP); `library/` and
  other vault paths stay writable; `CORE.md` accepts only the bounded
  writer.
- R6.5 Every contract write is a scoped git commit with writer
  attribution; every mutation logs the authorizing turn's `SourceRef`.
- R6.6 Validation errors return the corrected call shape; repeated
  invalid calls hit the bounded-failure path.

Acceptance: A6.1 "remember that X" searchable same day in inbox card;
A6.2 merge/forget without a real owner turn denied; A6.3 forgotten fact
never recalled/injected/promoted after restart; A6.4 schema rejection
with field info; A6.5 zone guards on all surfaces; A6.6 no stale-search
window.

## R7 — Eval gate, native import, vacuum (CHG-0212)

- R7.1 `hubctl memory import` parses existing `MEMORY.md`/`USER.md`
  into draft cards + directive candidates, `source: migrated`,
  reviewable before commit — nothing lands silently; crafted
  prompt-injection imports as inert data.
- R7.2 A deterministic eval fixture (seeded vault + scripted turns +
  assertions, no model) is the wave-1 gate: recall@k, supersede
  correctness, untrusted non-promotion, background non-capture, audience
  isolation, forget persistence. Same seed → same assertions; fixture
  content cannot leak into a production index. recall@4 ≥ 0.9.
- R7.3 A live eval runner (~40 RU/EN questions across LongMemEval
  categories) rides the `skilleval` HTTPRunner pattern through
  model-relay; judged against expected evidence refs; results recorded
  honestly — mocks never count as live.
- R7.4 `hubctl memory vacuum` prunes superseded/stale per policy;
  dry-run reports accurately and never deletes without the flag; cannot
  remove non-superseded content; git history always recoverable.
- R7.5 Nightly leaf lifecycle (CHG-0278), two fail-closed lanes after
  the rollup + period folds, each off by default:
  - FOLD (`memory_leaf_fold_bytes` >0): a completed day whose leaf
    exceeds the knob gets non-promotion-eligible block bodies stubbed
    in place through the governed writer — mem:id headers and
    interactive owner/agent blocks stay byte-identical; a verify pass
    must confirm id counts and eligible bodies before commit; a
    concurrent owner edit wins the WantSHA check and the fold skips.
  - EXPIRE (`memory_leaf_retention_days` >0, min 2): a leaf older than
    the window is removed via `git rm`+commit and its prov.db rows drop
    — only when the day-state is complete AND `summaries/daily/<day>.md`
    exists. An unverified day is kept and reported, never deleted.
  - Every action journals to `memory/REVIEW.md` under `## prune <day>`
    and merges into the localized morning digest; both lanes are
    idempotent and never touch today's leaf.
  - `hubctl memory prune` dry-runs the same Scan report; `--apply`
    executes; knobs ride the space settings and require `memory_vault`.

Acceptance: A7.1 reviewable drafts, nothing commits unreviewed; A7.2
deterministic suite green at the recall bound; A7.3 live results
recorded; A7.4 reproducibility; A7.5 vacuum honest dry-run; A7.6 fold
keeps eligible bytes verbatim + stubs audit blocks; A7.7 expire only
after complete rollup + summary; A7.8 unverified old leaves kept and
reported; A7.9 prune actions journaled + digested; A7.10 dry-run
mutates nothing.

## R8 — OpenKnowledge service (CHG-0209)

M32 amendment (SPEC-0098 #520): the direct-vault OpenKnowledge service and
R8/R9/R10 acceptance paths below are superseded while ToolHub is the sole
vault owner. `memory_open` now opens a separate read-only ToolHub view with
a one-hour private link and live read-right checks. The upstream editor and
write UI remain unavailable until they can use the governed API.

- R8.1 Feature `knowledge_ui` renders one `open-knowledge` compose
  service per space: image built via the hubctl BuildKit path with a
  pinned `@inkeep/open-knowledge` version, volume =
  `spaces/<id>/vault`, `ok init --no-mcp --no-skills` first boot,
  `replicas: 1`, private network only, no published ports by default.
- R8.2 `.okignore` at the vault root excludes `.git/`, `.graph/`,
  `.ok/`, `runtime/` sidecars; the vault root contains no secrets by
  layout.
- R8.3 External (hub) writes while a document is open are safe per the
  spike-verified OK conflict behavior (stale save rejected, conflict
  surfaced); no silent overwrite.
- R8.4 Human UI edits carry owner provenance and win the next rollup
  (`truth_pending` flow). Service down → vault/capture/recall
  unaffected.
- R8.5 Feature off → zero render/doctor effects; replica scale-out is
  rejected/pinned to 1.

Acceptance: A8.1 enabled space → single pinned replica on the space
network, vault mounted; A8.2 `.okignore` covers all non-knowledge
paths; A8.3 open-editor + hub write → both survive/conflict surfaces;
A8.4 owner UI edit wins next rollup; A8.5 deny-wins + replica pin.

## R9 — OK MCP read-only agent surface (CHG-0210)

- R9.1 `knowledge_ui` publishes the OK HTTP MCP endpoint
  (`http://open-knowledge:8080/mcp`) with a verified read-only subset:
  the tool list is fixed by verified effects at the pinned version —
  `exec`, `search`, `links`, `history`, `audit`, `lint` (read mode),
  `config`, `palette`, `skills` — excluding write verbs and side-channel
  verbs (`lint fix=true`, `preview_url`, `share_link`,
  `search ready=false`, `install`, `import`, `checkpoint`,
  `delete`, `move`, `edit`, `write`).
- R9.2 `knowledge_ui_write` is deferred: the grant is not rendered, a
  call to a write verb — direct MCP or via surface — is denied
  fail-closed with an honest unavailable; revocation removes surface and
  direct-call access.
- R9.3 The OK surface joins the projection/allowlist machinery
  (`tools.include`/`tools.exclude`, org `read_only_mcp`); org scope may
  grant read-only to members. OK down → honest degraded; `memory_search`
  /`memory_card` keep working.
- R9.4 No OK call may reach outside `spaces/<id>/vault`; path args are
  confined to the vault root.

Acceptance: A9.1 exactly the verified read subset visible; A9.2 write
verb denied at capability layer; A9.3 revocation propagates; A9.4
degraded honest; A9.5 reads cannot mutate; A9.6 path confinement.

## R10 — Knowledge-UI edge (CHG-0211)

- R10.1 `ui_port` (per-space, `agent.yaml`) renders
  `127.0.0.1:<ui_port>` publish + matching `OK_EXTERNAL_URL`; collisions
  diagnosed like browser/oauth ports; a non-loopback bind is a config
  validation error.
- R10.2 `hubctl status` surfaces the per-space `ui_port`; operations
  docs cover the SSH-tunnel flow and the no-public-exposure default.
- R10.3 The render leaves an explicit hook for the M18 exposure surface
  (named edge service / one-time `?t=` link design note + config
  placeholder) without implementing it.

Acceptance: A10.1 UI reachable via `ssh -L` only; A10.2 distinct ports
per space; A10.3 no non-loopback publish possible; A10.4 deferred-link
path documented, no partial auth.

## R11 — External provider adapters (CHG-0277)

- R11.1 Providers plug into the R1.3 seam only — a direct Go adapter
  implementing `memory.Provider`; the upstream `memory.provider` plugin
  path is never rendered (ADR D19). First adapter: Hindsight over its
  verified HTTP API (`bank_id` = opaque scope key; local embed endpoint
  default, cloud behind an explicit egress grant).
- R11.2 A provider is enabled per space only via an explicit managed
  capability grant; deny-wins; no silent fallback to another provider;
  disabling removes config and revokes egress cleanly.
- R11.3 `memorytest.Conformance` is green on the adapter: isolation,
  idempotent capture, forget, empty≠unavailable, provenance respected.
- R11.4 Migration is replay: `hubctl memory replay` streams a leaf
  range into `Provider.Capture`, resumable, idempotent; replay into a
  clean provider yields equal search results for fixture queries.
- R11.5 `hubctl memory export-honcho` imports pre-existing Honcho state
  into reviewable cards (`source: migrated`).
- R11.6 Provider down → honest degraded; vault path unaffected. Live
  protocol (flush-on-stop, session-switch recall, revocation) is
  recorded per CHG — mocks never count.

Acceptance: A11.1 adapter-path decision recorded; A11.2 grant-gated
enable, deny-wins, clean disable; A11.3 conformance green; A11.4 replay
equivalence; A11.5 honcho import reviewable; A11.6 degraded honesty.

## Global invariants

- V1 Nothing under `spaces/<id>` outside `vault/` is reachable by vault
  tools or OK; secrets/env never enter the vault by layout.
- V2 Every mutating capability is a separate grant plus a concrete owner
  instruction bound to the triggering turn's `SourceRef` (SPEC-0041).
- V3 All memory logic is hub-owned Go (ADR-0003); the model is a
  proposal oracle only; upstream Hermes is unpatched and
  `memory.provider` is never rendered.
- V4 Revocation removes rendered entries on next materialization and
  keeps vault content/history.
- V5 No live-model claims: unit/integration tests use fixture model
  responses; live acceptance is recorded per CHG.
- V6 `untrusted`/`system` provenance and non-interactive kinds are
  promotion-ineligible on every path; recalled content is marked and
  never re-captured.
- V7 Private-audience bytes (CORE, recall) never enter a group-scope
  run; scope keys are opaque and hub-minted.
