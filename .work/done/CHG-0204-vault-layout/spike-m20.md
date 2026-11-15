# M20.0 spike — evidence for the amended design (issue #418)

Conducted 2026-10-31 against a shallow checkout of the pinned upstream
(`nousresearch/hermes-agent@869228cab4a8276d3b4c78da9d9939670c47bd0f`,
kept under ignored `.work/tmp/hermes-pin/`) and `inkeep/open-knowledge`
HEAD (`.work/tmp/ok-pin/`).

## Q1: where does `ExecuteRequest.Instructions` land?

Hub path: `internal/communication/runtime.go` → `POST /v1/execute`
(`hubruntime.ExecuteRequest.Instructions`) → `internal/runtime/server_observe.go`
forwards it as `instructions` on the Hermes session-chat body.

Hermes side (`gateway/platforms/api_server.py:2987`):
`system_prompt = body.get("system_message") or body.get("instructions")` →
`run_kwargs[ephemeral_system_prompt]`.

`agent/system_prompt.py:638` and `agent/turn_context.py:960-1032`:
`ephemeral_system_prompt` is appended to the effective system prompt **at
API-call time only** — `messages` stays untouched, so it never persists to
session history. The prompt builder places it in the **context tier** —
after the stable prefix, before context files and the volatile tier — so an
unchanged value keeps the shared prefix cacheable; a per-turn varying value
would invalidate cache from the context tier onward.

**Decision (D12/D13):** CORE and the lane-1 recall block ride
`Instructions` — transient, unforgeable by message content, and byte-pinned
per job on the durable mapping (same mechanism as `StyleForRun`,
`internal/communication/tasks.go:783`). The pin makes a retry byte-identical,
which also keeps the cache behavior stable across retries. Message-embedded
delivery was rejected: it persists into history (visible to later capture)
and shifts with conversation position.

## Q2: `MemoryProvider` interface + plugin catalog at the pin?

Both exist. `agent/memory_provider.py` defines the `MemoryProvider` ABC:
`initialize → system_prompt_block / prefetch / sync_turn → tool dispatch →
shutdown`, plus `on_pre_compress` (v2 fail-closed checkpoint), `recall_status`,
`backup_paths`. `agent/memory_manager.py` drives it; ONE external provider at
a time via `memory.provider` config.

`plugins/memory/` catalog at `869228c`: `byterover`, `hindsight`,
`holographic`, `honcho`, `mem0`, `openviking`, `retaindb`, `supermemory`.

**Decision (D19, gates M20.11):** the upstream plugin path is *rejected* on
invariants, not availability: `sync_turn` hands the provider raw turns
outside our provenance capture; `prefetch`/`system_prompt_block` injects
recall outside the data-not-instructions frame and outside audience
confinement; injected provider tools create a second write path bypassing
`memory_card` + zone guards + owner-turn binding; provider state scopes to
`hermes_home`, not our opaque env×principal×audience scope. M20.11 ships a
direct Go adapter implementing `internal/memory.Provider`, first candidate
**Hindsight over its HTTP API** — verified at the pin via
`plugins/memory/hindsight/`: `hindsight_client` `aretain/arecall/areflect`,
`bank_id` scoping, `_DEFAULT_LOCAL_URL = http://localhost:8888`
(self-hostable embed mode, zero egress) vs cloud default. Holographic
(SQLite FTS5, local, `plugins/memory/holographic/`) stays the fallback
*reference* — it is a plugin, not an HTTP API, so it cannot ride our seam
directly; its retrieval shape informs the vault provider, not an adapter.

## Q3: cleanest native-`memory` removal when `memory_vault=on`?

Upstream gates: `_memory_toolset_requested` in `agent/agent_init.py:1250`
requires `"memory" in enabled_toolsets and not in disabled_toolsets`;
`memory_provider_tools_enabled` (`agent/memory_manager.py:85`) returns
False when `disabled_toolsets` contains `"memory"`.

Hub already renders `agent.disabled_toolsets` per space
(`internal/stack/render.go:169`, `internal/stack/hermes_capability.go:92`)
— the same mechanism strips `web` for unenabled spaces.

**Decision (D17):** `memory_vault=on` → `"memory"` joins the rendered
`disabled_toolsets` and `memory.memory_enabled/user_profile_enabled` render
false — one switch kills the tool surface AND the MEMORY.md/USER.md store
load; files stay on disk for the M20.7 import. No upstream patch needed.
Corollary: never render `memory.provider` plugin config (would both
resurrect provider tools and bypass the seam — see D19).

## Q4: OpenKnowledge external-write behavior, write scoping, `.okignore`

- **External write while a doc is open:** OK keeps both versions. A stale
  save is *rejected* and retained under `.ok/local/stale-external-writes.json`;
  conflicts surface via `.ok/local/conflicts.json` and its own shadow-git
  versions under `.git/ok/` (`docs/content/reference/what-open-knowledge-writes.mdx`;
  `packages/app/tests/integration/stale-external-write-rollback.test.ts`).
  So a hub write during an open editor session produces an explicit
  conflict, never a silent overwrite — the M20.8 acceptance gate encodes this.
- **Per-path write scoping:** none. `packages/server/src/mcp/tools/path-safety.ts`
  is only `resolveWithinRoot` — containment to the project root, no
  sub-tree grants. An agent write grant could hit `memory/daily|cards|summaries`
  directly → `knowledge_ui_write` stays deferred (M20.9 read-only);
  `memory_card` remains the only agent write path.
- **`.okignore`:** gitignore syntax via the `ignore` npm lib, honored
  instantly by index/search/MCP (`docs/content/features/ignore-patterns.mdx`);
  `.gitignore` exclusions honored automatically. Render `.okignore` with
  `.git/`, `.graph/`, `.ok/`, `runtime/` at scaffold time.

## Other anchors verified

- `job.Trigger` values in hub: `message`, `cron`, `goal`, `compact`,
  `wake`, `restart`, `auto-title` (`internal/communication/gateway*.go`,
  `occurrences.go`) → Kind mapping: interactive | routine | assignment |
  probe | goal | compaction | system.
- Style snapshot pin: `Spool.StyleForRun` (`internal/communication/tasks.go:783`)
  pins on `JobMapping` under lock — same durable-mapping mechanism the
  recall block reuses (`Recall`/`RecallSnapshotted` fields).
- Compaction entry points: `internal/communication/gateway_compact.go`
  (`Trigger: "compact"`) and `internal/compaction/compact.go` — the
  pre-compaction flush hook lands before the compress call.
- `internal/skilleval/http.go` `HTTPRunner` — the live-eval pattern M20.7
  reuses (prepared space + prompt → receipt).
- `google/jsonschema-go v0.4.3` already in `go.mod` — card validation needs
  no new dependency. No new dependencies anywhere in this milestone:
  BM25 is hand-rolled per ADR D6 (amended).
