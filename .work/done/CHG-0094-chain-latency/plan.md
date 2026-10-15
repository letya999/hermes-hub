# CHG-0094: Chain latency (M19 stage 2)

GitHub: issue [#263](https://github.com/letya999/hermes-hub/issues/263), milestone [M19](https://github.com/letya999/hermes-hub/milestone/20).

Chain: [ADR-0036](../../../docs/adr/ADR-0036-execution-chain-reliability.md) D8, D9
-> [SPEC-0053](../../../specs/active/SPEC-0053-execution-chain-reliability.md) R2
-> this plan -> [state](state.yaml). Evidence: [research](../CHG-0092-chain-stop-the-bleeding/research.md).
Previous: [CHG-0093](../CHG-0093-tool-surface-integrity/plan.md). Next: [CHG-0095](../CHG-0095-recovery-observability/plan.md).

## Goal

The user sees liveness within 1 s, warm answers in about 10 s, cold starts under
25 s, and the control plane is idle when nothing happens.

## Read before coding

ADR-0025 (serial per context), ADR-0015/SPEC-0014 (durable mappings), SPEC-0036
(channel rendering, multi-part outbox), SPEC-0037 (artifacts), SPEC-0053 R2.
Related open issues: #219 (live progress indicator), #214 (idle-by-activity).

## Slices

### S2.1 Early liveness and wake (R2.1)

Anchors: `internal/communication/communication.go` `handleUpdate`, `enqueueChannelJob`,
`media.go` `enqueueMediaJob`, `TelegramAPI` interface/`newTelegramAPI`;
supervisor `serveHTTP` (`/v1/leases` already acquires a runtime).

Change:
1. Add `SendChatAction(ctx, chatID, topicID, "typing")` to the Telegram API (Bot API
   `sendChatAction`, honor `message_thread_id`). Call it right after the update is
   accepted and authorized (never for unknown senders).
2. Add supervisor `POST /v1/wake` (or reuse `/v1/leases` with a short `LeaseWake`
   kind released after readiness): same `bindingFor` validation, no prompt text,
   idempotent, does not create a job record. Gateway calls it in a goroutine after
   the job is spooled; failures are logged only.

Tests: `TestAcceptedUpdateSendsTypingBeforeRun`, `TestUnknownSenderGetsNoChatAction`,
`TestWakeEndpointRejectsForeignBinding`, `TestWakeIsIdempotentAndCarriesNoText`.

### S2.2 One editable progress message (R2.2)

Anchors: `internal/communication/stream.go` (event deliveries `job-<id>-event-<hash>`),
`deliverOne`, Telegram API.

Change: replace per-event deliveries with one status delivery per job
(`job-<id>-status`) whose message ID is stored in the outbox receipt after the first
send; later events update text via `editMessageText`, rate-limited to one edit per
2 s per chat; the status message is deleted or finalized when the response is
delivered. Optional: `sendMessageDraft` when the chat is private and the bot API
version supports it (feature flag `HUB_TELEGRAM_DRAFTS`), finalize with `sendMessage`.
Slack: keep current behaviour (out of scope).

Tests: `TestProgressUsesSingleEditedMessage`, `TestProgressEditRateLimited`,
`TestStatusMessageFinalizedOnResponse`, `TestStatusSurvivesGatewayRestart` (receipt reused, no duplicate message),
`TestEditFailureFallsBackWithoutDuplicates`.

### S2.3 Event-driven spool (R2.3)

Anchors: `communication.go` `Run` tickers (100 ms control/delivery, worker ticker),
`Spool.ClaimJob`, `Spool.ClaimDelivery`, `findIdempotencyLocked` (linear scan),
`Spool.EnqueueDelivery`, `Spool.Enqueue`.

Change:
1. Add `Spool.notify chan struct{}` (buffer 1) signalled by every enqueue/complete;
   loops `select` on it plus a 1 s fallback ticker.
2. Idempotency index: write `mappings/by-key/<sha256(key)>.json` (pointer to job ID)
   atomically with the mapping; lookups read it directly; keep the scan as a one-time
   migration path for old mappings (build the index at startup).
3. Retention: background compaction of `done/`, `events/`, terminal `mappings/`
   older than `HUB_SPOOL_RETENTION` (default 336h) — never touch non-terminal records,
   approvals or receipts referenced by pending deliveries.
4. Move `e2e-acceptance` and `mock-*` data out of the production spool (separate dir
   configured only in test/dev harness).

Tests: `TestEnqueueWakesWorkerWithoutPoll` (fake clock, no ticker advance),
`BenchmarkIdempotencyLookup10k`, `TestIndexBuiltForLegacyMappings`,
`TestCompactionKeepsNonTerminal`, `TestCompactionKeepsPendingReceipts`, race test with `-race`.

### S2.4 Supervisor persistence and locking (R2.4)

Anchors: `internal/supervisor/supervisor.go` `persistLocked`, `finishJobGenerationLocked`,
`jobRecord` (stores `Request` incl. `Text`), `reconcile.go` `ReconcileHealth`
(holds `lockFor(key)` across `VerifyOwnership`, `docker inspect`, HTTP probes).

Change:
1. Strip `Request.Text` (and inbound content) before storing a job record; keep
   identity, idempotency and routing fields. Recovery never needs the prompt (it
   observes the run) — confirm in `RecoverMissingWork` and resume paths.
2. Persist only when status/generation/lease changes; progress events update memory.
3. Compact terminal job records older than 24 h to `{status, run_id, finished_at}`.
4. ReconcileHealth: snapshot under the lock, probe without it, re-acquire to apply
   with a generation check (already compared) — no Docker CLI under `lockFor`.

Tests: `TestJobRecordHasNoPromptText`, `TestProgressEventDoesNotPersist`,
`TestTerminalJobsCompacted`, `TestReconcileDoesNotBlockAcquire` (slow fake Command; Acquire completes concurrently).

### S2.5 Lazy browser and memory (R2.5)

Anchors: `internal/runtime/runtime.go` (starts Xvfb/fluxbox/x11vnc/websockify/chromium when `HUB_BROWSER`/`HUB_MEET`),
`internal/supervisor/supervisor.go` defaults (`cfg.Memory = "1g"`), browser MCP config in `internal/stack`.

Change: default memory `2g`. Start the display stack and Chromium on first use: a
tiny loopback launcher the browser MCP connects through, or start when the
`browser` MCP child first requests CDP (check Playwright MCP `--cdp-endpoint`
behaviour; if lazy CDP is not possible, start Chromium in a background goroutine
after Hermes readiness so it no longer delays readiness). Meet keeps eager start.

Tests: `TestTextOnlyContextStartsNoBrowser`, `TestBrowserStartsOnFirstUse` (or after readiness), docker smoke.

### S2.6 Stream-first observation (R2.6)

Anchor: `internal/runtime/server.go` `observeHermesRunWindow` (GET `/v1/runs/{id}` every 100 ms plus SSE).

Change: when the SSE channel is open, poll status every 2 s; on SSE close fall back
to 1 s polling. Terminal detection still comes from status.

Tests: `TestObservePollsSlowlyWhileStreamOpen` (count requests on fake Hermes), existing stall tests stay green.

## Red-team checklist

- `/v1/wake` cannot start another principal's runtime or leak runtime existence.
- `sendChatAction`/status edits go only to the job's delivery audience (chat + topic); forged callback cannot redirect edits.
- Compaction cannot delete a receipt needed for replay (crash during compaction, then restart).
- Prompt text absent from supervisor state, logs and diagnostics exports (grep a canary string).
- Lazy browser: the CDP port stays loopback-only; no new listener reachable from the agent network.

## Acceptance

S1-S4 and S9 from SPEC-0053 measured with `hubctl trace` or log timestamps over 30 jobs;
A2.1-A2.3. Record p50/p95 in state.yaml.

## Done checklist

- [ ] Tests red then green; `just check`; `just docker-check`.
- [ ] Red-team and SLO measurements recorded.
- [ ] docs/architecture.md (spool wakeups, retention), docs/operations.md (retention, memory) updated.
- [ ] GitHub issue updated; state done; folder moved to `.work/done/`.
