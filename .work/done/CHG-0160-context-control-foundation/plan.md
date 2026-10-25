# CHG-0160: Context-control foundation (M17)

Chain: `docs/adr/ADR-0075-dual-engine-context-control.md` D1-D4,D7-D10 ->
`specs/active/SPEC-0072-compression-policy-status.md` R1-R8 -> this plan -> `state.yaml`.
GitHub: #310. Next stage: `CHG-0161`, `CHG-0162`.

## Goal

Foundation for all three context issues: grant-gated engine selection, the
runtime sidecar transport (locks/transcript read/apply/probe), declarative
`compression:` policy, relay-routed `auxiliary.compression`, the between-jobs
sweep, and the shared compaction journal feeding `context_status`/`/usage`.

## Read before coding

1. AGENTS.md, README.md, docs/index.md
2. ADR-0075, SPEC-0072; ADR-0074 D4 (session continuity), SPEC-0071 R4 (runaway ceiling)
3. Pinned Hermes sources (pin `869228ca`): `agent/context_compressor.py`,
   `hermes_state_compression.py` (`try_acquire_compression_lock`,
   `acquire_session_turn_lease`, `archive_and_compact`, `get_compression_lineage`),
   `agent/auxiliary_client.py` (`_resolve_task_provider_model`, task config shape),
   `gateway/platforms/api_server_runs.py` (no compaction fields on `/v1/runs`)
4. `internal/selfsettings/selfsettings.go` (`compression.*` key-set/validators),
   `internal/stack/hermes_capability.go` (compression render ~L154),
   `internal/stack/render.go` (`auxiliary` ~L181),
   `internal/runtime/usage.go` (`SessionUsage`, lineage counting),
   `internal/runtime/model_relay.go`, `internal/runtime/runtime.go`
   (`hermesGatewayEnvironment`, spawn ~L420),
   `internal/agenttools/capability.go` (ToolSpec catalog), `tools_api.go` (~L161)

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only; the sidecar is plain Python-stdlib.
- The sidecar imports upstream libraries but never patches them; missing symbols
  yield `unsupported`, not crashes.
- No summary text or message content across the sidecar wire — counts, booleans
  and bounded fields only.
- `just check` before handoff; Docker gates when image/runtime code changes.

## Slices

### S1.1 Grant and setting plumbing (R1, R2, R7)

Anchors: `internal/selfsettings/selfsettings.go` validators;
`internal/stack/settings.go`/`render.go` declarative parse;
`internal/agenttools/capability.go` `HubToolsDefinition()`.

Change:
1. Reuse `selfsettings` `compression.*` validators for a declarative
   `compression:` block in `spaces/<user>/settings.yaml`; fail-closed on
   unknown/out-of-range keys with a named parse error.
2. Add bounded engine keys: `features.compact.engine`, `goals.engine`,
   `goals.judge`, `compression.hub_sweep.{enabled,threshold_tokens,cooldown_seconds,precompute}`,
   `goals.max_turns` — validated enum/range, persisted overlay.
3. Render `auxiliary.compression: {provider: custom, base_url: <relay>,
   api_key: ${OPENAI_API_KEY}, model: <granted logical model>}`; absent grant ->
   `provider: main` fallback. Render `auxiliary.goal_judge` the same way when granted.

Tests (red first):
- `TestCompressionBlockParsesBoundedKeys`: valid block renders; unknown key fails load.
- `TestAuxCompressionRendersRelayRoute`: config.yaml contains relay base_url + env-ref key, never a literal provider key.
- `TestAuxCompressionFallsBackToMain`: no dedicated grant -> main route.
- `TestRenderDefaultsUnchanged`: rendered compression/auxiliary blocks identical to today's defaults when settings empty.

### S1.2 Sidecar transport (D2, D7)

Anchors: `docker/Dockerfile` (image layer near L62);
`internal/runtime/runtime.go` spawn/env helpers; `internal/runtime/server.go`
private HTTP surface auth.

Change:
1. Add `docker/sidecar/hermes_sidecar.py`: JSON-RPC over stdin/stdout, subcommands
   `probe`, `lock`, `unlock`, `read_transcript`, `apply_transcript`,
   `scan_compactions`, `estimate_tokens`. Imports `hermes_state`/`agent.*` lazily;
   probes required symbols at start; missing -> `{"error":"unsupported"}`.
2. Go wrapper `internal/runtime/sidecar.go`: exec with timeout, JSON
   marshal/unmarshal, content-free error classes, hard output cap.
3. `archive_and_compact` is the only write path; `stamp_db_persisted_markers`
   after commit per upstream contract.

Tests (red first):
- `TestSidecarProbeReportsSymbols`: fake upstream tree -> capability map.
- `TestSidecarRefusesOversizedOutput`: cap enforced, process killed on deadline.
- `TestApplyRequiresLock`: apply without held lock -> `lock_required`.

### S1.3 Compaction journal and status (R5)

Anchors: `internal/runtime/usage.go` `SessionUsage`; `internal/stack` session store;
`internal/agenttools/tools_api.go` registration.

Change:
1. Journal rows `{trigger(auto_inloop|auto_sweep|manual|overflow),
   engine(hub|native|provider), method, tokens_before_est, tokens_after_est,
   failure_class, at, session_id}` appended via sidecar `scan_compactions`
   (raw-row carrier classification + lineage) after each completed job, and by
   direct writes from manual/sweep paths.
2. `context_status` ToolSpec (read, owner/context scoped): effective threshold
   + provenance (`declared|default`), observed compaction count, last event,
   current tokens with provenance (`measured|estimated`), `null` when unknown.
   `/usage` surfaces observed count + last event; in-place events come only
   from the journal — never fabricated.

Tests (red first):
- `TestJournalRoundTrip`: write/append/read bounded.
- `TestContextStatusUnknownIsNull`: no data -> explicit nulls.
- `TestCarrierScanCountsInPlace`: fixture transcript -> count.
- `TestNoCrossContextStatus`: foreign owner/context denied.

### S1.4 Between-jobs sweep (R4)

Anchors: job completion path (spool/supervisor finish hooks);
`internal/runtime/usage.go` estimate helpers.

Change:
1. After each completed job on a bound session: if `hub_sweep.enabled` and rough
   transcript estimate > `hub_sweep.threshold_tokens` and cooldown elapsed and
   turn lease free -> run the compact path (CHG-0161 engine per grant; until
   then, refuse-with-journal). No sweep during active run/compact. Sweep is the
   quality knob (below in-loop trigger); in-loop remains the survival defense.
2. Precompute speculation (`hub_sweep.precompute`, default on): estimate ≥
   ~85% of sweep threshold -> start a background summary through the relay and
   hold it per session. Commit only when it still describes the tail and the
   resolved model matches; ≤2 consecutive background failures then stop until
   next compaction; a late summary never delays a user-visible turn.
3. Post-compact notice: after any commit, next turn on that binding carries a
   bounded hub-generated notice (compaction + durable state re-state: active
   goal, pending items); no transcript/summary text inside.
4. Journal `trigger=auto_sweep` including refusal reasons and precompute
   outcomes.

Tests (red first):
- `TestSweepSkipsUnderThreshold`: no action below bound.
- `TestSweepNeverDuringRun`: held turn lease -> skip.
- `TestSweepCooldown`: second boundary inside cooldown -> skip.
- `TestPrecomputeStartsAtRatio`: estimate ≥ 85% of sweep threshold -> background
  summary started; below -> none.
- `TestPrecomputedSwapModelGuard`: resolved-model change discards held summary.
- `TestPrecomputeFailureCap`: 2 consecutive failures -> no new attempt until
  next compaction.
- `TestPrecomputedLateNeverBlocks`: user turn proceeds without waiting.
- `TestPostCompactNoticeBounded`: notice emitted once, allowlisted fields only.

## Red-team checklist

- Foreign owner/context sid or task -> denial before any container call.
- Sidecar output carries no message bodies; oversized/binary output killed.
- Stale lock holders: expired/dead-pid reclaim per upstream semantics only.
- Concurrent `/compact` + sweep + in-loop compress: single-writer via locks.
- Settings with renamed/typo keys: whole load fails, nothing half-applied.
- Relay absent/unreachable: compression config falls back or refuses named.

## Acceptance

SPEC-0072 A1..A8: bounded-key validation, relay-routed auxiliary render,
threshold-order contract test, journal + `context_status` + `/usage`, sweep
gating — all through real Go boundaries. Live: dev stack, real provider,
long session crosses threshold; verify journal and status reflect the real
in-loop compaction.

## Out of scope

Manual `/compact` command and `context_compact` tool (CHG-0161); `/goal`
(CHG-0162); micro_compact/idle/native-provider compaction; `context.engine`;
upstream contribution work.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates if needed.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated (specs index row on freeze, ops/config docs).
- [ ] Issue updated; state done; folder moved to `.work/done/`.
