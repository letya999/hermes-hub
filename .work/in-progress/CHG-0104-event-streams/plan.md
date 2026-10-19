# CHG-0104 — Stream event subscriptions

Extends CHG-0103 event subscriptions with `delivery=stream`: a hub-owned
long-poll loop per subscription that drains a blocking wait tool
(`wait_for_settled_message`-class connectors) instead of firing one
occurrence per cron tick.

## Problem

Poll-mode event subscriptions wake at cron cadence (minute granularity).
Connector wait tools already implement the push semantics — they block
server-side until a settled event arrives — but the 15s probe path and the
occurrence machinery make each poll a scheduled one-shot. For "answer this
person on my behalf for hours" the loop must run continuously.

## Design

- `Schedule.Delivery`: `""|"poll"|"stream"`; event-only; `stream` requires
  `probe.kind=mcp_tool`. Stream schedules produce no occurrences
  (`TickSchedules` skips them); `expression` stays as the dormant cadence
  should the owner switch back to poll.
- `streamSupervisor` (new `streams.go`, started from `Gateway.Run` via
  `startStreamLoops`): every 2s reconciles one `streamPoll` goroutine per
  live event+stream schedule; cancels on pause/delete/snooze/revoke,
  prunes exited loops, respawns re-enabled ones. Each loop re-reads its
  schedule, calls the wait tool through the existing `/v1/tool-call` path
  under a 30s budget, settles the batch via `settleStreamEvents`,
  best-effort requeues overflowed events by `chat_id`, then re-calls:
  immediately after a healthy round, exponential backoff (1s..30s) on a
  blocked round (probe error, lost authorization, malformed body).
- Wait-bound injection: `streamProbe` adds `max_wait_ms=20000` on
  settled-style tools and `timeout=20` on wait/new-message-style tools
  when the caller gave none, so calls fit inside the call budget;
  owner-supplied bounds are honored as-is and the stored probe is never
  mutated.
- `settleStreamEvents`: occurrence-less settle. Same durable EventState,
  dedup, caps, degraded tracking; deterministic batch seed
  `stream-<schedule>-<batchhash>` so a replayed batch dedupes. Returns
  overflowed events plus a blocked flag for loop pacing.
- `extractEvents`: connector sentinel `{"event":false}` yields zero events;
  `pending_chats` accepted as an items array; object events carry
  `chat_id` into `Event.Requeue` so requeue calls use the connector's
  `requeue_event(chat_id)` contract (`{"event_id":id}` fallback preserved).
  Object events without a textual key fall back to their bounded raw JSON
  so agent bundles retain routing fields (chat_id).
- `watch_subscribe(delivery=stream)`, `/watches` shows the mode.

## Acceptance

- A settled-burst tool result lands a sink delivery/agent job within the
  loop's next call — no cron involvement — and dedupes on replay.
- Stream loop survives empty results and probe errors; owner revocation
  fails closed; pause/delete stops the loop durably.
- Poll mode unchanged (default); all CHG-0103 tests stay green.
