# SPEC-0054: Proactive automation

Frozen: 2026-10-14. GitHub milestone 13 — issues #247, #248, #250, #251, #257.
Builds on SPEC-0013 occurrences and SPEC-0048 proactive kinds; all invariants
there keep holding. Every new surface is owner/context-scoped and durable.

## Stage 1 — Verified follow-ups (#251, #247 evidence)

- R1.1 A `check` occurrence whose probe errors retries bounded: the occurrence
  re-dues (`retry_count` increments, delay = retry_count minutes, cap
  `max_retries`, default 3) instead of settling `failed`. Exhaustion settles
  `failed`. `unknown` never reads as `false` — an unmet predicate is `skipped`,
  a failed probe is `failed`.
- R1.2 A `check` delivery names the verdict: the `expect` expression and the
  was/now values travel in the delivered text so the owner sees why it fired.
- R1.3 `probe.url` accepts `task:<task_id>` (no scheme, no fetch): the compared
  value is the owner's task state (`open`/`closed`/`archived`), resolved under
  the same envelope that owns the schedule. A foreign or missing task yields a
  probe error, never a synthetic state.
- R1.4 Reminder contract (implemented under SPEC-0048, evidenced here): a due
  reminder delivers its saved text through the outbox with zero model and
  zero runtime calls; occurrences dedupe by id; after downtime the
  `catch_up_window` policy applies and out-of-window occurrences settle
  `missed`; lost transport answers stay `uncertain`/failed-visible.

## Stage 2 — Watcher notification policy (#248)

- R2.1 `quiet_from`/`quiet_to` are owner-local HH:MM bounds validated at
  creation (both or neither). A notification produced inside the window gets a
  durable `not_before` at the window end on its Delivery; the outbox never
  drops it and never sends inside the window. Overnight windows
  (from > to) wrap midnight.
- R2.2 `daily_limit` (1..32) caps owner-facing notifications per schedule per
  owner-local day; the durable watch/event state counts. Excess occurrences
  settle `capped`; watch state still advances so nothing replays.
- R2.3 `notify_mode=agent` on a watcher admits one agent job per qualifying
  new revision — the job text is the saved input plus bounded was/now
  evidence, flagged untrusted. No delivery is enqueued; the model is never
  invoked for unchanged sources and never re-invoked for the same revision
  (occurrence-id idempotency).

## Stage 3 — Control-plane tool calls and mcp_tool probes (#257, #248)

- R3.1 `POST /v1/tool-call` on the ToolHub gateway takes `{tool, arguments}`.
  Auth equals `/v1/connectors`: an owner bearer pins its own envelope; the
  control-plane bearer selects an enrolled principal via `X-Hub-Principal`.
  The tool must be projected for that principal and carry a read-only
  effect — anything else is refused before invocation. The response is the
  bounded text content (<= 64 KiB) plus an explicit `is_error` flag; connector
  errors are distinguishable from empty results.
- R3.2 `probe.kind=mcp_tool` carries `{tool, args, requeue_tool}`. It is valid
  for `watcher`, `check` and `event` kinds. The hub calls R3.1 with the
  schedule owner's credential; the regex first-capture-group contract applies
  to the returned text. All http-kind validation stays unchanged.

## Stage 4 — Owner brief (#250)

- R4.1 `job_kind=brief` assembles one bounded snapshot at fire time: changed
  watch states since the last slot (the digest cursor semantics, honouring
  `source_ids`), unavailable sources marked unavailable — never reported as
  "no changes" — and the owner's open tasks. Owner/context scoping matches
  the digest kind.
- R4.2 An empty snapshot enqueues one deterministic no-changes delivery — no
  model. A non-empty snapshot admits one agent job whose text is the saved
  instruction plus the snapshot flagged untrusted; the model composes the
  brief inside the slot only. No model calls happen outside the slot.
- R4.3 Pause/revocation before fire blocks assembly and delivery through the
  shared re-authorization; restart dedup uses the occurrence id; a failed
  model run keeps the ordinary mapping status (`uncertain` visible).

## Stage 5 — Event subscriptions (#257)

- R5.1 `job_kind=event` schedules carry `sink` ∈ {`notify`, `agent`} and a
  probe. The agent surface gains `watch_subscribe` (kind=event fields),
  `watch_list`, `watch_pause`, `watch_delete` mirroring the routine HTTP API;
  `/watches` lists the owner's subscriptions deterministically.
- R5.2 Event extraction: `mcp_tool` results parse `{"events":[{"id":…}]}`;
  scalar/array results fall back to one event per top-level item with id =
  sha256 of the item; http/github probes treat a changed value as one event
  (id = new hash). The durable event state keeps a bounded seen-set
  (<= 512 ids, FIFO). Re-polled known ids never reach a sink; a crash between
  receipt and sink admission replays safely because seen is written only
  with the sink outcome.
- R5.3 Sinks: `notify` enqueues one delivery per new event (id
  `event-<occurrence>-<event>`), capped 8 per occurrence — overflow is counted
  in state, not lost silently. `agent` admits one job per occurrence bundling
  all new events flagged untrusted. A configured `requeue_tool` is invoked
  best-effort for events whose sink admission failed.
- R5.4 Authorization: the shared occurrence authorization runs at dispatch;
  the outbox re-check runs at delivery; a connector-side authorization
  failure settles the occurrence `failed` and marks the subscription
  `degraded` in its durable state.
- R5.5 The prepared telegram entry exposes `wait_for_new_message`,
  `wait_for_settled_message`, `requeue_event` and `incoming_feed_status` in
  `read_tools`. Event-capable connectors needing a persistent workload are an
  M13 connector-platform dependency — the hub contract above is complete
  without it and degrades to poll cadence.

## Acceptance

- A1 With the runtime unreachable, a due reminder still delivers; an
  unchanged watcher produces zero model calls and zero deliveries.
- A2 After a restart inside the catch-up window exactly one logical delivery
  exists per occurrence; outside it the occurrence is `missed`/`capped`
  visibly, not silently dropped.
- A3 A `check` on `task:` fires once when the predicate turns true and is
  `skipped`/auditable while false; a deleted task settles `failed`, never
  fired.
- A4 Quiet hours defer but never drop; daily_limit over-cap occurrences read
  `capped` in state.
- A5 `/v1/tool-call` refuses a write-effect tool and a foreign principal
  header under an owner bearer; `mcp_tool` probe errors never advance the
  baseline.
- A6 A `brief` slot with no changes sends no model call; with changes sends
  exactly one agent job even across a restart between assembly and dispatch.
- A7 Two pollers (or a crash mid-fanout) never deliver one event twice;
  `requeue_tool` is invoked on sink failure; a revoked grant stops the next
  poll effect and marks the subscription degraded.
