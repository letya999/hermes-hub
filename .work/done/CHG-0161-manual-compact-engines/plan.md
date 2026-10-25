# CHG-0161: Manual compact — dual engine (M17)

Chain: `docs/adr/ADR-0075-dual-engine-context-control.md` D1-D3,D7-D9 ->
`specs/active/SPEC-0073-manual-compaction.md` R1-R9 -> this plan -> `state.yaml`.
GitHub: #309. Previous stage: `CHG-0160`. Next stage: `CHG-0162`.

## Goal

Owner-invoked `/compact` from channels and ToolHub: grant-selected `hub` or
`native` engine, upstream argument semantics (`here N`/`focus`/`preview`),
turn-boundary-only mutation, allowlisted responses.

## Read before coding

1. AGENTS.md, ADR-0075, SPEC-0073
2. Pinned Hermes: `agent/context_compressor.py` (`compress(force, focus_topic)`),
   `hermes_cli/partial_compress.py` (`parse_partial_compress_args`,
   `extract_compress_flags`, `split_history_for_partial_compress`,
   `rejoin_compressed_head_and_tail`, `summarize_compress_preview`),
   `agent/conversation_compression.py` (commit/rollback, anti-growth guard,
   `_salvage_or_refuse_grown_transcript`)
3. `internal/communication/gateway.go` `dispatchChannelCommand` (~L436),
   `internal/communication/task_commands.go`, `internal/supervisor/control.go`,
   `internal/runtime/control.go` (envelope/binding validation),
   `internal/runtime/sidecar.go` (CHG-0160), `internal/runtime/model_relay.go`,
   `internal/agenttools/capability.go`, `tools_api.go`

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. No `/compress` text is ever sent to `/v1/runs`.
- Shared engine contract: same envelope auth, session resolution, busy policy,
  response allowlist — engine differs only in the pipeline body.

## Slices

### S2.1 Engine interface and selection (R2, R3)

Anchors: `internal/runtime/control.go` validation shape;
`internal/supervisor/control.go` relay surface; new `internal/runtime/compact.go`.

Change:
1. `CompactEngine` interface: `Preview(ctx, session, opts) / Apply(ctx, session,
   opts)` returning the allowlisted result struct; `busy_policy` honored by caller.
2. Engine resolution: grant `context.compact` allowed-engines ∩
   `features.compact.engine`; deny ungranted engines with named error + audit.
3. Session = caller's stable conversation id only; turn-lease + compression-lock
   acquisition before any mutation; release on all paths.

Tests (red first):
- `TestEngineDeniedWithoutGrant`: named refusal + audit row.
- `TestCompactOnlyBetweenTurns`: held turn lease -> refuse/queue, no mutation.
- `TestNeverCreatesSession`: missing session -> refusal, no session row minted.

### S2.2 Hub engine pipeline (R5, R6, R8)

Anchors: `internal/runtime/compact.go`; `internal/runtime/model_relay.go`;
sidecar `read_transcript`/`apply_transcript`/`estimate_tokens`.

Change:
1. Go pipeline is the cheapest-first cascade (ADR-0075 D3): read raw transcript
   -> head/tail protection -> `prune_only` attempt (tool-output pruning; if the
   post-prune estimate fits, commit with zero LLM calls) -> `precomputed_swap`
   if a held summary still describes the tail (same resolved model) -> one
   `summary` call through the relay -> one shrink-and-retry (smaller tail) ->
   `drop_checkpoint` (explicit bounded-loss marker). Refuse-if-grows +
   alternation check before every apply; atomic apply; journal
   `trigger=manual, engine=hub, method=<stage>`.
2. `prune` arg forces the prune stage only — selective hygiene, no summary.
3. `preview`: same cascade minus apply; reports projected savings and
   `would_method` (which stage would fire).

Tests (red first):
- `TestHubCompactRefusesGrowth`: grown result -> refusal, transcript untouched.
- `TestHubCompactAlternation`: output preserves role alternation at the seam.
- `TestPreviewMutatesNothing`: transcript hash identical after preview.
- `TestRelayFailureNamesClass`: relay down -> `failure_class=summary_unavailable`, transcript intact.
- `TestPruneOnlyZeroCalls`: fitting transcript commits with no relay call,
  `method=prune_only`.
- `TestPruneArgForcesStage`: `prune` on an over-fit transcript prunes or
  refuses — never summarizes.
- `TestCascadeOrder`: stages run in order; each skipped stage journaled.
- `TestDropCheckpointNamesLoss`: terminal failure path writes the explicit
  marker, `method=drop_checkpoint`.
- `TestPreviewWouldMethod`: preview reports the stage the apply would take.

### S2.3 Native engine (R8, R9)

Anchors: sidecar new subcommand `native_compress` calling
`ContextCompressor.compress` + `partial_compress` helpers + upstream anti-growth
guard before `archive_and_compact`.

Change:
1. Sidecar `native_compress --session <id> [--here N] [--focus t] [--preview]`:
   lock -> load -> upstream compress -> upstream refuse-if-grows -> apply ->
   report. Missing upstream symbols -> `unsupported`.
2. Engine maps `unsupported` to a named refusal (do not silently fall back).

Tests (red first):
- `TestNativeCompressTranslatesArgs`: `here/focus/preview` map to upstream calls.
- `TestNativeUnsupportedOnMissingSymbol`: probed absence -> `unsupported` refusal.

### S2.4 Channel command and ToolHub tools (R1, R4, R7)

Anchors: `internal/communication/gateway.go` `dispatchChannelCommand`;
`internal/agenttools/capability.go` `HubToolsDefinition()`; `tools_api.go`.

Change:
1. `case "compact", "compress"` parse `[here N|focus <text>|prune|preview]`;
   dispatch through supervisor -> runtime compact path; busy -> deterministic
   refusal or bounded queue entry (never drops the pending user turn).
2. `context_compact` (write, owner/context scoped, audited) and `context_status`
   catalog entries + handlers bound to the same engine path.

Tests (red first):
- `TestCompactCommandParsesUpstreamArgs`: `here 5`, `focus x`, `prune`,
  `preview`, bare.
- `TestBusyDeterministic`: in-flight run -> single documented outcome.
- `TestContextCompactGrantAndAudit`: ungranted caller denied; granted call audited.
- `TestNoLiteralSlashToRuns`: command never reaches `/v1/runs` as prompt text.

## Red-team checklist

- Foreign owner/context session id -> denial before locks.
- Focus text / args with injection attempts -> treated as literal summary hint.
- Concurrent manual + sweep + in-loop: single writer via locks; losers refuse.
- Summary text exfiltration: assert absence in tool result, journal, logs.
- Replayed compact request (retry/dup) -> idempotent response.
- Preview flood: preview takes no write lock, bounded read only.

## Acceptance

SPEC-0073 A1..A9 on real Go boundaries; dev-stack live: real session on a real
provider compacted via both engines; `context_status` shows the journal entry;
busy session produces the documented outcome; transcript survives a forced
summary-backend failure.

## Out of scope

Auto-compact policy internals (CHG-0160); goal mode (CHG-0162); upstream
endpoint contribution; `micro_compact`/idle compaction; provider-native
compaction; `context.engine` plugins.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates if needed.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated; issue #309 updated; state done; move to `.work/done/`.
