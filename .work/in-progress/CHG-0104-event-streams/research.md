# Research — stream subscriptions

## Connector (letya999/telegram-mcp @ local tree)

`telegram_mcp/tools/events.py` implements the full event-side contract:

- A Telethon `NewMessage(incoming=True)` handler records incoming
  private non-bot messages into `_pending_msgs` per chat; group traffic
  only via `TELEGRAM_INCOMING_GROUPS` + mention/reply gating;
  `TELEGRAM_ALLOWED_CHAT_IDS` scoping.
- `wait_for_settled_message(settle_ms=6000, max_wait_ms=50000, chat_id)`
  BLOCKS until a chat's burst settles, returns
  `{"event": true, chat_id, name, username, message_count,
  first_message_id, last_message_id, kind, topic_id, sender_id}` and
  consumes the burst; `{"event": false, "reason": "timeout"}` otherwise.
- `wait_for_new_message(timeout, chat_id)` — non-consuming variant
  returning `{"event": true, "pending_chats": [...]}`.
- Pending bursts, dedup seen-set and a delivered-outbox persist to
  `TELEGRAM_PENDING_FILE` across connector restarts; `requeue_event
  (chat_id)` returns a delivered burst to pending (idempotent no-op when
  nothing delivered). NOTE: requeue takes `chat_id`, not `event_id` —
  hub requeue args must prefer the event's `chat_id`.
- Burst objects carry no `id`/`event_id`/`message_id` → hub-side identity
  is the content hash (fine: requeued bursts get fresh timestamps, and
  seen only contains admitted events).

## ToolHub call path

`MCPBackend.CallEnv` opens a fresh MCP session per call against the
workload endpoint; the container itself stays alive between calls while
the budget's idle TTL is refreshed — so frequent wait calls keep the
Telethon session warm and its in-memory pending buffer accumulating.
Pending state also persists connector-side, so even a reaped workload
loses nothing. No persistent-workload mode is required for v1.

## External precedent (web)

- chigwell/telegram-mcp PR #130 — the exact `wait_for_new_message` /
  `wait_for_settled_message` contract this repo's pinned fork carries:
  "replaces blind per-minute polling: call, process, call again".
- telegram-agent-mcp `wait_for_reply` — same blocking long-poll pattern
  with addressed/human filters.
- Thrum MCP `wait_for_message` — background waiter waking the main agent;
  documents single-waiter semantics (ours: one loop per schedule,
  disjoint chat filters recommended).
- claude-tg — `tg_get_messages(waitSeconds)` long-poll feeding sessions.
- Telethon docs — `NewMessage(incoming)` handlers + update-gap catch-up:
  server-side update replay covers connector restarts.

## Hub wiring points

- `toolCallProbe` already enforces read-effect + owner-pinned credential +
  15s bound — reused as the loop's transport; a wait bound ≤10s fits.
- `authorizeOccurrence` gates each iteration (requires `Trigger=="cron"` —
  stream jobs keep it).
- `occurrenceFixture`-style tests inject `toolCall` directly; the stream
  settle path is a pure spool function + a gateway loop with injectable
  call/sleep seams.
