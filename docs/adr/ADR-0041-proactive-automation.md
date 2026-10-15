---
description: Accepted contracts for hub-initiated proactive work — verified-source follow-ups, watcher notification policy, control-plane tool calls, owner briefs and event subscriptions.
last_verified: 2026-10-14
---
# ADR-0041: Proactive automation (M12)

Status: accepted, 2026-10-14, by the owner's instruction to implement GitHub
milestone 13 (issues #247, #248, #250, #251, #257).

Requirements: [SPEC-0054](../../specs/active/SPEC-0054-proactive-automation.md).
Execution: [CHG-0099](../../.work/in-progress/CHG-0099-followup-hardening/plan.md),
[CHG-0100](../../.work/in-progress/CHG-0100-watcher-policy/plan.md),
[CHG-0101](../../.work/in-progress/CHG-0101-tool-call-probes/plan.md),
[CHG-0102](../../.work/in-progress/CHG-0102-owner-brief/plan.md),
[CHG-0103](../../.work/in-progress/CHG-0103-event-subscriptions/plan.md).

## Context

SPEC-0048 already froze the deterministic proactive kinds: `reminder`,
`watcher`, `check`, `digest` and `script` fire on the schedule -> occurrence ->
outbox pipeline with no model or runtime at fire time. The milestone issues ask
for the remaining product contracts on top of that machinery:

- #251 wants a bounded retry for unverifiable conditions, an auditable reason
  for firing and a hub-verifiable task-state predicate.
- #248 wants quiet hours, a daily cap and opt-in semantic escalation, plus
  sources that live behind connectors rather than plain URLs.
- #250 wants one opt-in brief per slot from authorized snapshots.
- #257 wants durable subscriptions that turn connector events into sinks
  (notify / agent / routine) — near-real-time without a warm runtime.
- #247's delivery contract is already implemented; what remains is evidence
  and the documented overdue/uncertain policy.

Everything must keep the SPEC-0013/0048 invariants: durable occurrence,
idempotent dispatch, re-authorization at dispatch and delivery, per-owner
scoping, no new database/queue/framework.

## Decisions

1. **D1 One pipeline for every proactive kind.** New kinds are fields on the
   existing `Schedule`/`RoutineOccurrence` records — no separate subscription
   store, daemon or queue. An event subscription is a `job_kind=event`
   schedule; its occurrences probe and fan out to sinks. Pause, snooze,
   delete, catch-up, owner scoping and occurrence-id dedup come free.
2. **D2 Verified follow-ups get bounded retry and a task predicate.** A
   `check` occurrence whose probe fails retries at most `max_retries` times
   (default 3) by re-dueing the occurrence, then settles `failed` — `unknown`
   never reads as `false`. Check deliveries state the evaluated predicate and
   was/now values. `probe.url` may be `task:<id>` for a hub-verified task
   state (`open`/`closed`) instead of http; same probe contract, no fetch.
3. **D3 Watcher policy is schedule-local.** `quiet_from`/`quiet_to` (owner
   timezone, HH:MM) defer the generated delivery to the window end via a
   durable `not_before` on the Delivery — nothing is dropped. `daily_limit`
   caps owner-facing notifications per owner-local day; excess occurrences
   settle `capped` (auditable) while watch state still advances.
   `notify_mode=agent` is the opt-in semantic escalation: a qualifying new
   revision admits one agent job carrying the saved text plus the bounded
   evidence, flagged untrusted. The model never sees an unchanged source.
4. **D4 Control-plane tool calls go through one new ToolHub endpoint.**
   `POST /v1/tool-call` on the ToolHub gateway accepts the same bearer contract
   as `/v1/connectors`, resolves the principal exactly like `connectorAuth`,
   refuses any tool whose projected effect is not read-only, and returns a
   bounded text result. `probe.kind=mcp_tool` uses it for watcher, check and
   event sources — credentials never leave the ToolHub workload.
5. **D5 The brief is deterministic assembly plus opt-in synthesis.**
   `job_kind=brief` builds one bounded owner snapshot (watched-source changes
   since the last slot, unavailable sources marked, open tasks) at fire time.
   An empty snapshot delivers a fixed no-changes line — zero model. A
   non-empty snapshot admits one agent job flagged untrusted so the model
   composes the brief inside the slot, not continuously.
6. **D6 Event subscriptions fan out through existing sinks.**
   `job_kind=event` occurrences extract discrete events per probe kind —
   `mcp_tool` results parse `{events:[{id,…}]}`, http/github changes count as
   one event — and dedupe by a bounded seen-set in durable state. `sink=notify`
   enqueues one delivery per new event (bounded per occurrence); `sink=agent`
   admits one job bundling the events flagged untrusted. A connector
   `requeue_tool` is invoked for events that fail sink admission. Telegram
   event tools join the prepared entry's `read_tools`; the persistent workload
   lifecycle they need is a connector-platform dependency (M13), documented
   not built here.
7. **D7 Reactive delivery is a supervised bounded long-poll, not a stream.**
   `delivery=stream` on an event schedule (`mcp_tool` probe only) produces no
   occurrences; a gateway supervisor keeps one goroutine per live stream
   schedule issuing repeated bounded wait-calls through the same
   `/v1/tool-call` path — 30s budget, caller-or-injected wait bounds — and
   settling every batch through the identical durable pipeline. This reaches
   seconds-level latency where the connector already buffers events
   (`wait_for_settled_message`-class tools) without a persistent-workload
   mode or push ingress, which remain connector-platform work. Poll stays
   the default; the stream loop re-reads its schedule each round so pause,
   delete, snooze or a lost grant stops it on durable state, and blocked
   rounds pace with bounded exponential backoff rather than hot-spinning.

## Alternatives

- A standalone subscription store/daemon: rejected — duplicates pause,
  ownership, dedup and catch-up machinery that the occurrence pipeline already
  proves.
- Webhook ingress for third-party providers: out of scope per issue #257;
  polling through ToolHub keeps credential egress inside the existing fence.
- A generic workflow engine for conditional follow-ups: rejected by #251
  itself and by the no-unsolicited-framework rule.

## Invariants kept

- No model or runtime call on tick, poll, predicate or unchanged source.
- Owner/context scoping on every record; re-authorization before dispatch
  and before delivery; revoked grants stop the next effect.
- Durable ids dedupe every replay path; uncertain transport outcomes stay
  visible instead of claiming exactly-once.
