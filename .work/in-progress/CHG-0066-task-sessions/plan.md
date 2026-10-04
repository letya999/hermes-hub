# CHG-0066 Task sessions and Telegram DM topics (issue #167)

## Problem

One Telegram DM maps to exactly one deterministic Hermes session
(`sessionIDFor` = hash of context + conversation). `/session` can only report it;
there is no way to run several durable conversations for different tasks in one
DM, or to bind a Telegram private-chat topic to its own task.

## Design

- `Task` is a durable owner/context-scoped record in the communication spool
  (`tasks/`). Binding is `(channel, verified peer, chat, topic) -> task ->
  conversation -> Hermes session`. ConversationID carries the task:
  `telegram-<chat>` for the default task (legacy path, unchanged session),
  `telegram-<chat>-<taskID>` for named tasks — a new conversation gets its own
  deterministic Hermes session for free via `sessionIDFor`.
- The implicit `default` task needs no file until it is mutated (style, rename);
  materialized lazily. Legacy DM history stays on `telegram-<chat>`.
- Telegram `direct_messages_topic.topic_id` on inbound messages resolves to the
  bound task; posting in an unbound topic adopts it (explicit verified-peer
  action). A per-chat `current` pointer routes root-DM messages; `/task use`
  switches it. Outbound sends use `direct_messages_topic_id` — never
  `message_thread_id` (upstream #55265: they are not interchangeable).
- `Job`, `JobMapping`, `Delivery`, `Schedule` gain `task_id`/`topic_id`, copied
  at admission and on every reply path (stream receipts, final, errors,
  approvals, artifacts, voice, routines). Model routing never overrides the
  verified inbound audience.
- `/cancel`/`/approve` now authorize the caller in the job's own conversation
  (external identity still proves the sender), so an owner can act on a task
  job without switching to it first.
- Commands: `/task` (current), `/task new|use|rename|archive`, `/tasks`.
  Slack and other channels keep their legacy conversation; channel-thread
  binding stays an explicit later step.

## Out of scope

- Slack thread binding UX, Telegram forum-group topics, inline buttons.
- Parallel execution across tasks — context serialization per
  (principal, context) from ADR-0025 is unchanged.
