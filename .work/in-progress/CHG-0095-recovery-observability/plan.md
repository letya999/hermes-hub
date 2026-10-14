# CHG-0095: Recovery and visibility (M19 stage 3)

GitHub: issue [#264](https://github.com/letya999/hermes-hub/issues/264), milestone [M19](https://github.com/letya999/hermes-hub/milestone/20).

Chain: [ADR-0036](../../../docs/adr/ADR-0036-execution-chain-reliability.md) D2, D10
-> [SPEC-0053](../../../specs/active/SPEC-0053-execution-chain-reliability.md) R3
-> this plan -> [state](state.yaml). Evidence: [research](../CHG-0092-chain-stop-the-bleeding/research.md).
Previous: [CHG-0094](../CHG-0094-chain-latency/plan.md). Next: [CHG-0096](../CHG-0096-control-plane-simplification/plan.md).

## Goal

Any single component may die at any moment and every job still ends with exactly
one answer or one failure that names its cause. We learn about broken
dependencies from the system within minutes, not from the user.

## Read before coding

SPEC-0014, SPEC-0015, SPEC-0016 (durable mappings, approvals, lifecycle), SPEC-0031
(automatic diagnostics), SPEC-0049 (mock channel), ADR-0036 D2/D10, SPEC-0053 R3.
Hermes API server docs: Runs API idempotency (see research). Related issues:
#193 (mock harness), #206 (incident replay), #74 (Hermes reconnect).

## Slices

### S3.1 Explicit job state machine (R3.1)

Anchors: `internal/supervisor/supervisor.go` `jobRecord`, `beginJob`, `bindJobGeneration`,
`finishJobGenerationLocked`, `terminalRunStatus`; `internal/communication/job_mapping.go`.

Change: add `Phase` enum to the supervisor job record (`accepted, waking, admitted,
running, waiting_approval, completed, failed, cancelled, interrupted`) with a
transition table function `nextPhase(from, event) (to, ok)`; illegal transitions are
rejected and logged. `uncertain` becomes a flag (`Unconfirmed bool`, `UnconfirmedSince`),
not a phase. Keep JSON backward compatible (old records map to phases on load).

Tests: table test `TestJobPhaseTransitions` (every legal/illegal pair),
`TestLegacyRecordsMapToPhases`.

### S3.2 Reconciler for unconfirmed jobs (R3.2)

Anchors: supervisor `RecoverMissingWork`, `resume` (`/v1/resume`), runtime `observe`,
`admitHermesRun` (`hermesRequest(... request.IdempotencyKey)` already sends the key).

Change: a reconcile loop (existing ticker in `cmd/supervisor`) picks unconfirmed jobs
older than 30 s: if `run_id` is known -> observe via runtime `/v1/observe`; if not ->
re-post `/v1/runs` with the **same** `Idempotency-Key` (Hermes replays the original
`run_id`, HTTP 202 `Idempotency-Replayed: true`) then observe. On terminal result,
record it and let the gateway deliver once (existing outbox idempotency). After the
run budget, finish as `failed` with cause `timeout`. Gateway: stop sending the
"Исход задачи пока неизвестен" message; show it only if the budget expired without
confirmation, with the cause class.

Tests: `TestReconcileReplaysIdempotencyKeyWhenRunIDUnknown` (fake Hermes counts creates == 1),
`TestReconcileObservesKnownRun`, `TestReconcileDeliversOnce`,
`TestUnconfirmedPastBudgetFailsWithCause`, `TestGatewayNoUncertainNoticeBeforeBudget`.

### S3.3 Poison update quarantine (R3.3)

Anchor: `communication.go` polling loop (`if err := g.handleUpdate(ctx, update); err != nil { return err }`).

Change: on error write `quarantine/<update_id>.json` (update ID, error class, time; no
message text unless content logging is enabled), advance the offset, continue. Cap the
directory (oldest removed beyond 1000).

Tests: `TestHandleUpdateErrorQuarantinesAndContinues`, `TestQuarantineHasNoContentByDefault`.

### S3.4 Synthetic canary (R3.4)

Anchors: mock channel (`internal/communication/mock.go`, SPEC-0049), routines
(`occurrences.go`), owner notices (deterministic deliveries).

Change: built-in system routine `canary` per configured owner context, disabled by
default, enabled with `HUB_CANARY_INTERVAL=5m`. Each tick injects a mock message
with a fixed deterministic prompt that requires one hub tool call (for example
`file_list` on an empty canary directory) through the real chain, using its own
conversation (never the user's). Checks: admission, model reply, tool called,
manifest match from readiness, ToolHub reachable, delivery to mock outbox. Records
`canary.jsonl` (bounded). On a new failure class, send one owner Telegram notice
(deterministic text, no model); on recovery, one recovery notice.

Tests: `TestCanaryUsesOwnConversation`, `TestCanaryNoticeOncePerFailureClass`,
`TestCanaryRecoveryNotice`, `TestCanaryDisabledByDefault`.

### S3.5 Structured logs and `hubctl trace` (R3.5)

Anchors: `internal/diagnostics/http.go`, `log.Printf` call sites (about 65), SPEC-0031 diagnostics export.

Change: introduce `internal/diagnostics/log.go` returning a `*slog.Logger` with JSON
handler, UTC, fields `component, job_id, run_id, principal`. Migrate HTTP middleware and
the gateway/supervisor job lifecycle lines first (do not rewrite every log call).
`hubctl trace <job_id> [--user]`: read supervisor log file, `docker logs --since`
for the user's containers, Hermes `logs/agent.log|gateway.log|errors.log` in the user
space, ToolHub audit; filter by job/run/session IDs; sort by time; redact with the
existing redaction helpers; print a table.

Tests: `TestSlogJSONHasRequiredFields`, `TestTraceMergesSourcesInOrder` (fixtures),
`TestTraceRedactsSecretsAndContent`, `TestTraceRefusesOtherPrincipal`.

### S3.6 Operational metrics in `/status` (R3.6)

Anchors: `/status` handler, usage journal (`usage_journal.go`), job mappings.

Change: compute last-24h p50/p95 latency (received -> delivered), counts of
unconfirmed/failed, runtime restarts (supervisor state), average input tokens/call
(usage journal) and show them under a "Здоровье" block.

Tests: `TestStatusHealthBlock`, `TestStatusHealthWithNoData`.

## Chaos matrix (A3.1) — must be run and recorded

| Kill target | At admission | Mid-run | Mid-delivery |
|---|---|---|---|
| runtime container | | | |
| ToolHub container | | | |
| communication-hub container | | | |
| supervisor process | | | |

Each cell: expected = one answer or one cause-named failure; no duplicate send; no
duplicate external effect (use a counting fixture tool). Run with a scripted helper
(`scripts/chaos-matrix.ps1` or a Go integration test behind `-tags integration`).

## Red-team checklist

- Idempotency replay cannot be used to fetch another principal's run (key isolation per runtime auth; verify Hermes keys are per credential).
- Canary prompt cannot be altered by users; canary cannot call write tools.
- `hubctl trace` never prints message text, tokens, or other principals' data.
- Quarantine files cannot fill the disk (cap) and contain no content by default.

## Done checklist

- [ ] Tests red then green; `just check`; `just docker-check`.
- [ ] Chaos matrix filled with evidence; A3.2-A3.4 recorded.
- [ ] docs/operations.md (trace, canary, quarantine), SPEC-0031 cross-reference updated.
- [ ] GitHub issue updated; state done; folder moved to `.work/done/`.
