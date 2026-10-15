# SPEC-0053: Execution chain reliability, speed and visibility

Frozen: 2026-10-13, after the owner's approval of the M19 plan. Supplements SPEC-0012,
SPEC-0014, SPEC-0015, SPEC-0016, SPEC-0019, SPEC-0031, SPEC-0037 and SPEC-0049
without rewriting their history. Design: [ADR-0036](../../docs/adr/ADR-0036-execution-chain-reliability.md).
Research and prior art: [CHG-0092 research](../../.work/done/CHG-0092-chain-stop-the-bleeding/research.md).

Requirement IDs (`R<stage>.<n>`) and acceptance IDs (`A<stage>.<n>`) are referenced
by the CHG plans, tests and GitHub issues of milestone M19. A requirement is met only
with the listed evidence; mock-only tests never prove live acceptance.

## Service level objectives

Measured on the owner's local dev stack with the configured provider, warm runtime
unless stated, over at least 30 interactive jobs and 24 h of routine traffic.

| ID | Objective | Target | Measured by |
|---|---|---|---|
| S1 | First visible liveness (chat action or status) after an accepted Telegram update | p95 <= 1 s | gateway log `accepted_at` -> `liveness_sent_at` |
| S2 | Simple text answer, warm runtime | p50 <= 10 s, p95 <= 25 s | `hubctl trace` received -> final delivered |
| S3 | Cold start to admitted run | p95 <= 25 s | supervisor `waking` -> `admitted` |
| S4 | Photo/document answer, warm | p50 <= 30 s | trace |
| S5 | `uncertain` shown to the user for runs shorter than the run budget | 0 | gateway outcome counters |
| S6 | Unplanned Hermes restarts / unclean exits | 0 per 24 h | Hermes `lifecycle_ledger`, supervisor log |
| S7 | Tool-surface drift after any `render`/`up`/model change | 0 missing manifest tools | readiness report |
| S8 | Input tokens per model call, ordinary interactive turn | p50 <= 15 000 | Hermes `sessions` counters |
| S9 | Gateway idle CPU | <= 5 % of one core | `docker stats` sample over 5 min |
| S10 | Detection of a broken dependency (model, ToolHub, hub child, supervisor, network) | <= 5 min, one owner notice | canary journal |

## R0: Stop losses (CHG-0092)

1. Streaming hops (runtime -> supervisor -> gateway) have no total client timeout.
   Each hop applies an idle deadline (default 60 s) reset by any event. The runtime
   emits `run.heartbeat` at most every 15 s while a run is active. Non-stream calls
   keep bounded total timeouts.
2. One run budget (`HUB_RUN_BUDGET`, default 30 min) is read in one place per binary
   and is the maximum for every hop; no hop may cut a run earlier than the budget.
3. The gateway retries a job submission on dial/connect errors and HTTP 503 with
   exponential backoff (1 s base, 20 s cap) until the cold-start budget (default
   4 min) expires. Responses after admission are never re-submitted by this path.
4. Every container stop/restart issued by the supervisor uses `--time 30` or more.
   A runtime with any job, stream, approval or lifecycle lease is not restarted by
   reconcile or by projection markers.
5. Warm TTL default is 30 min (`-warm-ttl`); an owner always-on pin keeps the
   context runtime running without a job.
6. The supervisor runs under a service manager with automatic restart, listens on
   loopback or an explicitly configured private address, and reads its token from
   a file with owner-only permissions. `0.0.0.0` requires an explicit flag and a
   firewall note in operations.md.
7. Routine occurrences use a conversation ID derived from the schedule ID, not the
   chat task conversation. Identical routine failure text is delivered at most once
   per schedule until a success or a changed error.
8. A Telegram `getUpdates` 409 Conflict (second poller on one bot token) stops
   polling, logs the conflict and notifies the owner once; `hubctl up` refuses a
   dev stack whose bot token equals another running stack's token.
9. Every internal HTTP request carries `X-Hub-Job-ID` when a job exists, and
   `diagnostics.HTTP` logs it with `run_id` and principal when known. All Go
   service logs are UTC.

Acceptance:
- A0.1 A synthetic run lasting 5 min through real supervisor and runtime binaries
  completes and is delivered once; no `uncertain` record appears.
- A0.2 Supervisor stopped for 20 s while a message arrives: the job is admitted
  after restart without a user-visible error.
- A0.3 Reconcile with an active lease never issues `docker restart`.
- A0.4 Killing the supervisor process: the service manager restarts it within 10 s
  and active runtimes are adopted, not duplicated.
- A0.5 A routine failing 10 times produces one chat message.
- A0.6 Two gateways with one token: the second stops polling and the owner gets one notice.

## R1: Tool surface integrity and context (CHG-0093)

1. `policyVersion` is computed only from rights-bearing fields (ADR-0036 D5). The
   exact field list is a tested table; changing model ID, ports, UI or transport
   settings leaves it unchanged.
2. When the rights hash changes during an operator render/up, active ToolHub bindings
   of the same principal/context/runtime migrate atomically to the new version.
   Disabled and revoked bindings do not migrate. The migration is idempotent and
   audited; a concurrent writer causes a retry, never a partial store.
3. `ListProjectedTools` logs each excluded binding once per projection revision with
   reason (`policy_mismatch`, `disabled`, `revoked`, `stale`, `degraded`, `not_found`).
4. Render writes `generated/tool-manifest.<env>.json` listing expected projected and
   direct tool names per context. Runtime readiness (`/v1/health`) reports
   `tool_surface: ok|degraded` and the missing names. `/status` shows them in plain
   language. Degraded tool surface never triggers a restart.
5. At startup the runtime runs the hub child self-test through the pinned Hermes
   loader path; failure makes readiness `degraded` with a specific reason.
6. The configured model ID must exist in the provider catalog at runtime start;
   otherwise readiness is `degraded: model_not_found` and the owner is told once.
7. Hub tools return tool-level failures as MCP `isError` results, never as
   transport errors, so the Hermes circuit breaker does not count them.
8. Static run instructions move into the stable prompt prefix (SOUL/system
   section) rendered per context; the per-run `instructions` field carries only
   per-run data (style, voice availability).

Acceptance:
- A1.1 Changing `model` in `agent.yaml` and running `up` keeps every previously
  visible tool visible.
- A1.2 A store with bindings under an old policy is migrated by `up`; revoked ones stay revoked.
- A1.3 Removing an env var required by the hub child yields `/status` naming the tool
  and reason within one readiness probe; no restart occurs.
- A1.4 Three consecutive tool-level hub errors do not hide other hub tools.
- A1.5 Routine runs no longer append messages to the chat session (session row counts).
- A1.6 S8 holds on 30 interactive turns.

## R2: Latency (CHG-0094)

1. On an accepted update the gateway sends `sendChatAction(typing)` and calls the
   supervisor wake endpoint concurrently with spooling. The wake is idempotent and
   authorized like a lease request; it never carries prompt text.
2. Progress for one job is a single message edited in place (or a Telegram draft
   when supported); edits are rate-limited to one per 2 s per chat. The final answer
   is sent through the outbox as today.
3. Gateway loops (workers, delivery, control) wake on in-process signals; the
   fallback poll interval is at least 1 s. Mapping lookup by idempotency key is a
   direct file read. Terminal spool records older than the retention window
   (default 14 days) are compacted; acceptance/test data never lives in the
   production spool volume.
4. Supervisor job records omit request text; terminal records older than 24 h are
   compacted to outcome-only form; state persists only on job/runtime state change.
   Health probes do not hold the per-runtime acquisition lock during Docker CLI calls.
5. Chromium, Xvfb, x11vnc and websockify start only when the browser feature is
   enabled and on first browser use; the runtime memory default becomes 2 GiB.
6. The runtime observes Hermes primarily through the run event stream; status
   polling is a fallback at 2 s intervals.

Acceptance: S1, S2, S3, S4 and S9 hold; A2.1 a text-only context starts no browser
processes; A2.2 a 10 000-mapping spool resolves idempotency in constant time
(benchmark); A2.3 compaction never removes non-terminal records.

## R3: Recovery and visibility (CHG-0095)

1. The supervisor journal holds one explicit state machine per job:
   `accepted -> waking -> admitted -> running -> waiting_approval -> terminal`.
   `uncertain` is a reconciler input, never a delivered outcome before the budget.
2. The reconciler resolves an uncertain job by re-posting with the same
   idempotency key and observing the run; on success it delivers the result once.
   After the budget it delivers one failure with a cause class.
3. A failing `handleUpdate` quarantines that update (bounded file with reason) and
   advances the offset; the polling loop continues.
4. A synthetic canary job runs through the mock channel and real chain every 5 min
   (configurable, off by default in CI). It checks admission, model reachability,
   tool manifest, ToolHub, hub child and delivery, records latency, and sends one
   deterministic owner notice per new failure class and one recovery notice.
5. Go services log structured UTC lines (stdlib `log/slog`, JSON handler) with
   `job_id`, `run_id`, `principal`, `component`. `hubctl trace <job_id>` merges
   gateway, supervisor, runtime, ToolHub and Hermes log lines into one ordered,
   redacted timeline.
6. `/status` reports p50/p95 latency, uncertain/failed counts, restarts and token
   averages for the last 24 h from local journals.

Acceptance:
- A3.1 Chaos matrix: kill runtime, ToolHub, communication-hub or supervisor at
  admission, mid-run and mid-delivery; each job ends with exactly one delivered
  answer or one cause-named failure; no duplicate external side effect.
- A3.2 Malformed update does not stop polling.
- A3.3 Stopping cliproxy produces one owner notice within S10 and one recovery notice.
- A3.4 `hubctl trace` reconstructs a photo job end to end.

## R4: Control-plane simplification (CHG-0096)

Design only until each proposal has its own accepted ADR: supervisor in a Linux
container (D12a), embedded store (D12b), Hermes pin raise (D12c, may proceed under
this spec once the Hermes contract gate passes). Each proposal must state isolation
impact, migration, rollback and measured benefit against the SLO table.
