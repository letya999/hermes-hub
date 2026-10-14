---
description: Accepted end-to-end time, lifecycle, tool-surface, context, wakeup and observability contracts for the Telegram-to-Hermes execution chain.
last_verified: 2026-10-13
---
# ADR-0036: Reliable, fast and observable execution chain

Status: accepted, 2026-10-13, by the owner's approval of the M19 plan. D1-D11 are
accepted for implementation. D12 lists proposals that each need their own ADR.

Requirements: [SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md).
Execution: [CHG-0092](../../.work/in-progress/CHG-0092-chain-stop-the-bleeding/plan.md),
[CHG-0093](../../.work/in-progress/CHG-0093-tool-surface-integrity/plan.md),
[CHG-0094](../../.work/in-progress/CHG-0094-chain-latency/plan.md),
[CHG-0095](../../.work/in-progress/CHG-0095-recovery-observability/plan.md),
[CHG-0096](../../.work/in-progress/CHG-0096-control-plane-simplification/plan.md).
Evidence and prior art: [research](../../.work/in-progress/CHG-0092-chain-stop-the-bleeding/research.md).

## Context

The 2026-10-13 audit of the live local dev stack (docker logs, `.local/supervisor.log`,
`spaces/local/hermes/logs`, `supervisor.json`, ToolHub `store.json`, Hermes `state.db`)
and of twenty recent agent sessions found that the chain is slow and unreliable
because of contracts *between* components, not because of the model:

- The supervisor proxies the run stream through one `http.Client` with a 130 s total
  timeout while gateway and runtime allow 30 min. Every run longer than about two
  minutes becomes `uncertain` (observed `duration_ms=130124`, `131278`).
- Pre-admission transport failures (supervisor down, runtime restarting) immediately
  become `uncertain`; only HTTP 503 is retried.
- The system restarts its own runtime: reconcile remediation (disk `degraded`, fixed
  in CHG-0091), projection-change restarts, a 1 s stop grace on `docker restart`, and
  a five-minute warm TTL. Fifteen unclean Hermes exits were logged in one day.
- `policyVersion` hashes the whole rendered config. Any setting change orphans every
  ToolHub binding; `ListProjectedTools` skips them silently. 131 bindings exist under
  14 policy versions; only one is active under the current version.
- The hub stdio child environment is rendered on the Windows host and consumed in a
  Linux container; three separate env/path defects reached production in one day.
- The pinned Hermes MCP circuit breaker disables a whole server for 60 s after three
  errors, including tool-level errors, hiding every hub tool.
- Routine occurrences share the chat conversation and session; one session reached
  940 messages and about 52 k input tokens per model call.
- The gateway polls file queues every 100 ms under one spool mutex (about 60 % idle
  CPU), scans every mapping for idempotency lookups and never compacts.
- The supervisor rewrites a 1.6 MB `supervisor.json` (including prompts) with fsync
  on every stream event under its global mutex and runs as a manually launched
  Windows process listening on `0.0.0.0`.
- There is no correlation ID across services, logs mix UTC and local time, and the
  user sees "outcome unknown" or "could not complete" without a cause.

Existing decisions remain binding: isolated per-context runtimes (ADR-0014), ToolHub
as the only authorization point (ADR-0013, ADR-0031), durable job/run mappings
(ADR-0015), serial execution per context (ADR-0025), pinned upstream Hermes without
patches, no unsolicited database/queue/framework dependency.

## Decisions

1. **D1 One time budget, idle deadlines for streams.** A single run budget (default
   30 min) is defined once and propagated gateway -> supervisor -> runtime. Streams
   never use a total `http.Client.Timeout`; each hop applies an idle deadline that
   resets on every event, and the runtime emits a heartbeat event at most every
   15 s. Dial/connect failures and HTTP 503 before admission are retried with
   backoff inside the cold-start budget, because the job provably did not start.
2. **D2 `uncertain` is internal and transient.** After admission, a lost stream is
   reconciled by re-submitting the same `Idempotency-Key` to Hermes `/v1/runs`
   (durable 24 h replay of the original `run_id`) and observing `GET /v1/runs/{id}`.
   The user receives one terminal outcome. A terminal failure names its cause class
   (model, tool, runtime, timeout, policy) without secrets. Unknown external side
   effects remain explicitly unknown; nothing is blindly re-executed.
3. **D3 Lifecycle never kills work it does not have to.** Every stop/restart uses a
   grace of at least 30 s. A runtime holding a job/stream/approval lease is not
   restarted. Only `unavailable` readiness for the configured window remediates.
   Tool projection changes are delivered with MCP `notifications/tools/list_changed`
   and do not restart Hermes; a restart marker is applied only when idle. The warm
   TTL is configurable with a 30 min default; an owner may pin a context always-on.
4. **D4 The supervisor is a supervised service.** It runs under a service manager
   with restart-on-failure and a file log, listens on loopback or a private interface
   reachable by the gateway, and reads its token from an owner-only file, never from
   command-line arguments or a checked-in script.
5. **D5 Capability policy versions cover rights only.** The policy hash is computed
   from rights-bearing fields (features, `tools:`, organization ceiling, MCP
   definitions, grants). Model, ports, UI and transport settings do not change it.
   When it changes because of an operator-approved render, active bindings migrate
   atomically to the new version; disabled/revoked bindings never migrate. A binding
   excluded from projection is logged once per revision with its reason.
6. **D6 Readiness means capability readiness.** Render writes a tool-surface manifest
   per context. Runtime readiness requires: configured model present in the provider
   catalog, the hub child self-test (`tools/list` plus a no-op call) passing, and the
   manifest being a subset of the tools Hermes actually exposes. A mismatch reports
   `degraded` with the missing tool and reason to `/status`; it never restarts.
7. **D7 Context hygiene.** Routines, watchers, probes and synthetic checks use their
   own conversation/session, never the user's chat session. Stable run instructions
   live in the stable prompt prefix; per-run text is minimal. Input tokens per model
   call are measured and budgeted.
8. **D8 Event-driven wakeups, polling only as a fallback.** Writers signal in-process
   channels; durable files remain the source of truth; a slow fallback poll repairs
   lost signals. Idempotency lookup is O(1) by key-derived file name. Terminal spool
   records and supervisor job records are compacted after a retention window.
   Supervisor state persists on state transitions only and never stores prompt text.
9. **D9 The channel shows liveness immediately.** On accepted input the gateway sends
   a chat action within 1 s and wakes the runtime in parallel with spooling.
   Progress uses one editable status message or a Telegram draft stream; the final
   answer is a durable `sendMessage` through the outbox. Separate progress messages
   are not sent.
10. **D10 Correlation and visibility.** Every internal HTTP request carries
    `X-Hub-Job-ID`; every service logs UTC structured lines with `job_id`, `run_id`
    and principal. `hubctl trace <job_id>` assembles one timeline. A synthetic
    canary through the mock channel exercises the real chain periodically and sends
    one deterministic owner notice per failure class (no model call).
11. **D11 Priority lanes.** Interactive user jobs are admitted before routine
    occurrences of the same context. Routine errors are deduplicated per source and
    rate-limited; deterministic checks never invoke a model.
12. **D12 Proposals requiring separate ADRs.** (a) Move the supervisor into a Linux
    container using the Docker socket proxy and stdlib HTTP, removing Windows path
    materialization and `host.docker.internal`. (b) Replace file spool/supervisor
    state with an embedded single-writer store if D8 metrics remain insufficient.
    (c) Raise the Hermes pin to a version with the MCP breaker fixes (tool-level
    errors do not trip the breaker; per-server `breaker_threshold`) after the
    Hermes contract gate passes; this is a normal pin update, not a patch.

## Alternatives and consequences

- Temporal, Restate or DBOS would add a server and SDK for guarantees already
  available from existing durable mappings plus Hermes run idempotency.
- Postgres/Redis queues add an always-on dependency to a single-host control plane;
  the measured cost is polling and scans, not file durability.
- Raising the 130 s timeout alone moves the cliff; idle deadlines remove it.
- Restarting Hermes on tool changes is simpler but kills runs; `list_changed` is
  already implemented and verified (architecture.md).
- Keeping a whole-config policy hash is safe but makes tool loss the default
  outcome of routine maintenance. Narrowing the hash does not widen any grant;
  migration copies only active bindings already approved by the operator.
- A shared runtime would be faster to warm but breaks the isolation boundary.

Isolation and authorization do not change: per-context containers, owner-scoped
mounts, ToolHub per-call authorization and credential injection remain the only
paths. All new logs and notices are bounded and secret-free.
