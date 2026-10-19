# CHG-0092: Stop losses in the execution chain (M19 stage 0)

GitHub: issue [#261](https://github.com/letya999/hermes-hub/issues/261), milestone [M19](https://github.com/letya999/hermes-hub/milestone/20).

Chain: [ADR-0036](../../../docs/adr/ADR-0036-execution-chain-reliability.md) D1-D4, D7 (routine part), D10 (IDs), D11 (dedupe)
-> [SPEC-0053](../../../specs/active/SPEC-0053-execution-chain-reliability.md) R0
-> this plan -> [state](state.yaml). Evidence: [research](research.md).
Branch: `feat/m19-execution-chain-reliability`. Next stage: [CHG-0093](../CHG-0093-tool-surface-integrity/plan.md).

## Goal

No run shorter than the run budget is reported `uncertain`; the system never
kills a runtime that holds work; the supervisor survives crashes; routines stop
polluting chat sessions and spamming errors.

## Read before coding (in this order)

1. `AGENTS.md`, `README.md`, `docs/architecture.md` (Service boundary, Target scale-to-zero).
2. ADR-0036, SPEC-0053 R0, this plan, `research.md` local evidence table.
3. Code anchors listed per slice. Use Serena `find_symbol`/`find_referencing_symbols`
   or `grep` to list **every caller** of a function before changing it.

## Preconditions and rules

- The working tree may contain uncommitted work of CHG-0090/CHG-0091 owned by another
  task. Never reset, stash or reformat it; re-read dirty files before editing.
- Write the failing test first (repository TDD hook enforces this), then the fix.
- No new dependencies. Go stdlib only. Run `just check` before handing off.
- Do not touch the live stack except for the live acceptance step, and then only via
  `hubctl build/up` or `just docker-check` per AGENTS.md.

## Slices

Implement in order. Each slice is one commit with its tests.

### S0.1 Idle deadlines and heartbeat (R0.1, R0.2)

Anchors:
- `internal/supervisor/supervisor.go` `New` (cfg.HTTP with `HUB_SUPERVISOR_JOB_TIMEOUT` 130 s),
  `execute` (forwards `/v1/execute`), `relayRunStream`, `forwardRuntimeRequest`.
- `internal/communication/runtime.go` `HTTPRunner.client`, `timeout`, `defaultHTTPRunTimeout`.
- `internal/runtime/stream.go` `writeStream`; `internal/runtime/server.go` `observeHermesRunWindow`.

Change:
1. Add a small stdlib helper (e.g. `internal/runtime/idle.go`): `IdleReader(r io.Reader, idle time.Duration, cancel func())`
   that cancels the request context when no bytes arrive for `idle`. Reuse it in
   supervisor `relayRunStream` and gateway `RunOutcome` stream branch.
2. Supervisor: split clients. `streamHTTP` has `Timeout: 0` and a transport with
   `ResponseHeaderTimeout: 30s`; the per-request context carries the run budget.
   Keep the existing bounded client for readiness, restart, usage, artifact calls.
3. Runtime: while a run is active, emit `{"last_event":"run.heartbeat"}` frames every
   15 s from `writeStream` (ticker goroutine guarded by the emit mutex).
4. Heartbeat frames are transport-only: supervisor `relayRunStream` and gateway
   `RecordStreamEvent` must skip persistence and delivery for `run.heartbeat`.
5. Introduce `HUB_RUN_BUDGET` (default 30m) read by one helper per binary; replace
   `defaultHTTPRunTimeout`, `hermesRunTimeout` and the 130 s client usage with it.

Tests to write (red first):
- `TestRelayRunStreamOutlivesLegacyClientTimeout` (supervisor): fake runtime streams
  events for longer than a deliberately tiny legacy timeout; terminal event recorded `completed`.
- `TestRelayRunStreamIdleDeadlineCancels`: fake runtime stops sending; stream ends
  after idle deadline with `uncertain` recorded (not earlier).
- `TestRuntimeStreamEmitsHeartbeat` (runtime): fake Hermes holds a run; heartbeat frames appear at the interval (inject the interval).
- `TestHeartbeatNotPersistedOrDelivered` (supervisor + communication): heartbeat frames never change job records or create deliveries.
- `TestRunBudgetSingleSource`: all three binaries resolve the same budget from env.

### S0.2 Retry only provably-unadmitted submissions (R0.3)

Anchor: `internal/communication/runtime.go` `RunOutcome` loop (`attempt >= 3`, 503 only, `unavailableRetryDelay`).

Change: classify `client.Do` errors. Dial/connect refused, DNS failure, or TLS
handshake before any request byte was accepted (`*net.OpError` with `Op == "dial"`,
`errors.Is(err, syscall.ECONNREFUSED)`) and HTTP 503 retry with exponential backoff
(1 s base, x2, cap 20 s) until a cold-start budget `HUB_COLD_START_BUDGET` (default 4m).
Any error after the response header (or a write error after connect) stays `ErrUncertain`.

Tests:
- `TestRunOutcomeRetriesConnectionRefusedUntilSupervisorReturns`: listener opened after 3 s; job completes; exactly one admission.
- `TestRunOutcomeDoesNotRetryAfterHeaders`: server sends headers then closes; result `uncertain`, one request.
- `TestRunOutcomeColdStartBudgetExpires`: always refused; ends with explicit failure (not `uncertain`) naming `runtime_unavailable`.

### S0.3 Lifecycle never kills leased work (R0.4)

Anchors: `internal/supervisor/reconcile.go` `ReconcileHealth` (remediation `m.command(ctx, "restart", ...)`),
`supervisor.go` `applyRuntimeRestartLocked` (`m.command(restart, "restart", container)`),
`restartRequestHTTP`, `idle stop` (`"stop", "--time", "30"` already correct).

Change:
1. Every `restart` call passes `"--time", "30"` (constant `stopGraceSeconds`).
2. Reconcile remediation requires `entry.Leases == 0` in addition to the existing
   `unavailable` window, consecutive-uncertain count and cooldown.
3. Investigate why live containers report `StopTimeout=1`
   (`docker inspect <container>` -> `Config.StopTimeout`); if set by any run
   argument, set `--stop-timeout 30` in both `docker run` arg lists in `supervisor.go`.
4. Verify hub-runtime forwards SIGTERM to Hermes and waits up to 25 s before exiting
   (`internal/runtime/process_unix.go` `stopProcess`, runtime main loop); add the wait if absent.

Tests:
- `TestRestartCommandsUseGrace`: fake `Command` records args; every `restart`/`stop` has `--time 30`.
- `TestReconcileNeverRestartsLeasedRuntime`: unavailable readiness + 2 uncertain + lease held -> no restart.
- `TestRuntimeStopWaitsForHermesExit` (unix-only build tag if needed).

### S0.4 Warm TTL and always-on (R0.5)

Anchors: `cmd/supervisor/main.go` flag `warm-ttl` (5m), `internal/supervisor/pins.go` (`SetPin`, `/v1/pins`).

Change: default `warm-ttl` 30m. Document in `docs/operations.md` how the owner sets an
always-on pin via the existing `/v1/pins` (or add `hubctl runtime pin --user`
if no CLI exists — check `cmd/hubctl` first; reuse, do not duplicate).

Tests: `TestDefaultWarmTTLIsThirtyMinutes`; existing pin tests stay green.

### S0.5 Supervisor as a supervised service (R0.6)

Anchors: `cmd/supervisor/main.go` (`-listen` default `127.0.0.1:8876`, `-auth`), `.local/bin/run-supervisor.cmd` (local only, never commit).

Change:
1. Add `-auth-file` flag: read token from file; refuse if the file is group/world
   readable on unix; on Windows require the file under the user profile or `spaces/.control`.
   `-auth` on the command line logs a deprecation warning.
2. Refuse `-listen 0.0.0.0:*` unless `-allow-wildcard-listen` is set.
3. First verify from a container that `host.docker.internal:8876` reaches a supervisor
   bound to `127.0.0.1` on Docker Desktop. If not, document binding to the Docker
   Desktop/WSL adapter address and a Windows firewall rule restricted to that subnet.
4. Add `scripts/install-supervisor-task.ps1`: registers a Windows Scheduled Task
   (at logon, restart on failure every 10 s, stdout/stderr to `.local/supervisor.log`)
   and a matching systemd unit example in `docs/operations.md` for Linux hosts.

Tests: `TestAuthFileRejectsWorldReadable` (unix), `TestWildcardListenRequiresFlag`,
`TestAuthFlagWarns`.

### S0.6 Routine isolation and error dedupe (R0.7, D11)

Anchors: `internal/communication/occurrences.go` (`admitted.ConversationID = taskConversation(...)`),
`communication.go` `worker` (error delivery keys `job-<id>-error`), `tickProactiveContext`.

Change:
1. Occurrence jobs get `ConversationID = "routine-" + scheduleID` (stable per schedule);
   keep `TaskID/TopicID` for delivery routing only. Mapping checks (`MatchesJob`)
   must compare the new conversation consistently.
2. Per schedule, remember the last failure digest (sha256 of normalized text) in
   the schedule state; enqueue the error delivery only when it differs or after a success.
3. In `ClaimJob`, when both an interactive job and an occurrence are pending for the
   same context, claim the interactive one first.

Tests:
- `TestOccurrenceUsesScheduleConversation` (session isolation).
- `TestRoutineRepeatedFailureDeliveredOnce` (10 failures -> 1 delivery; success then failure -> 2).
- `TestInteractiveJobClaimedBeforeOccurrence`.
- Migration: existing in-flight occurrences with the old conversation still settle.

### S0.7 Second poller guard (R0.8)

Anchors: `communication.go` polling loop (`GetUpdates`), `newTelegramAPI`.

Change: detect HTTP 409 from `getUpdates`; log `telegram-conflict`, enqueue one owner
notice (dedupe key `telegram-conflict`), back off 60 s and keep retrying silently.
`hubctl up`: refuse when another running compose project on the host has the same
`TELEGRAM_BOT_TOKEN` hash (compare sha256 of the token via existing generated env; never print it).

Tests: `TestGetUpdatesConflictNotifiesOwnerOnce`; `TestUpRejectsDuplicateBotToken` (hubctl, synthetic tokens).

### S0.8 Correlation IDs and UTC (R0.9)

Anchors: `internal/diagnostics/http.go`, every outbound `http.NewRequest` in
`internal/communication/runtime.go`, `internal/supervisor/supervisor.go`, `internal/runtime/inbound.go`.

Change: set `X-Hub-Job-ID` on outbound calls when the job ID is known; `diagnostics.HTTP`
logs `job_id=` (validated with `identity.ValidID`, else omitted). `log.SetFlags(log.LstdFlags | log.LUTC)`
in each `cmd/*/main.go`.

Tests: `TestHTTPLogsJobIDWhenValid`, `TestHTTPLogDropsInvalidJobID` (no injection of newlines/secrets).

## Red-team checklist (must be attempted and recorded in state.yaml)

- Header injection: `X-Hub-Job-ID` with newline, 10 KB value, another principal's job ID -> log omits it; no authorization effect.
- Retry abuse: a request that reached Hermes then the socket closed must not be re-submitted by S0.2 (prove with a counting fake Hermes).
- Heartbeat spoofing: a frame claiming `run.heartbeat` with a different `job_id` is rejected by the existing identity check.
- Restart-under-lease: hold an approval lease and trigger every restart path (reconcile, `/v1/restart`, projection marker) -> no restart.
- Token handling: `-auth-file` path traversal, symlink to a world-readable file, empty file.
- Routine isolation: a routine cannot read or append to the user's chat session (session row counts before/after).

## Acceptance (SPEC-0053 A0.1-A0.6)

Unit/integration tests above, then live on the dev stack:
1. A0.1 send a prompt that takes > 3 min (for example a long research request); one answer, no "Исход задачи неизвестен".
2. A0.2 stop the supervisor task for 20 s, send a message, restart; one answer.
3. A0.3/A0.4 kill `hub-supervisor.exe` during an active run; task restarts it; run completes once.
4. A0.5 create a watcher on an unreachable URL; one error message over 20 min.
5. A0.6 start a second gateway with the same token in a scratch compose project; owner gets one notice.
Record evidence (log lines, timestamps; no secrets, no message content) in `state.yaml`.

## Out of scope

Policy hash, manifest, Telegram status UI, queue wakeups, canary, containerized
supervisor (later CHGs).

## Done checklist

- [ ] All slice tests written red, then green.
- [ ] `just check` passes, coverage >= 85 %.
- [ ] `just docker-check` (or pulled-image path) when runtime image code changed.
- [ ] Red-team items recorded.
- [ ] Live acceptance recorded.
- [ ] docs/operations.md updated (supervisor service, warm TTL, pins).
- [ ] GitHub issue updated with evidence; state.yaml `status: done`; move folder to `.work/done/`.
