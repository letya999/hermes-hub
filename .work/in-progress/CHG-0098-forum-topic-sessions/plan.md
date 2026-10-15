# CHG-0098: Forum topic per session (M11 Telegram & Channel UX, issue #220)

Milestone: GitHub milestone 12 ("M11 — Telegram & Channel UX"),
branch `feat/m12-telegram-channel-ux` from `dev`.

## Goal

In a forum-enabled Telegram group where the bot is an admin with
manage_topics, `/new` creates a dedicated forum topic per session instead of
binding the caller's topic. Session lifecycle mirrors the topic lifecycle:
delete removes the topic, an adopted auto-title renames it. Archive/restore
(close/reopen) land with the archive command surface in issue #217.

## Slices

### S1 /new mints a bot-owned forum topic (issue #220)

Anchors: `internal/communication/task_commands.go` `newTaskCommand`;
`internal/communication/tasks.go` `RebindTaskTopic`;
`internal/communication/communication.go` `TelegramAPI`/`telegramAPI`.

Change:
1. `CreateForumTopic` Bot API method returning `message_thread_id`.
2. `RebindTaskTopic` moves a task's binding to the fresh topic.
3. Group `/new` creates the task unbound, then mints and binds the topic;
   on failure the task binds to the inbound topic (pre-existing fallback).

Tests (red first):
- `TestNewInForumGroupCreatesTopic`: topic created with session name, task
  rebound, confirmation posted inside the new topic, follow-up post routed.

### S2 Auto-title renames the topic

Anchors: `communication.go` `maybeAutoTitle`; `EditForumTopic`.

Tests (red first):
- `TestNewForumTopicFollowsAutoTitle`: adopted title calls editForumTopic.

### S3 Fallback when the bot cannot manage topics

Tests:
- `TestNewForumFallbackBindsInboundTopic`: create failure keeps the
  inbound-topic binding and reply target (regression; green on S1 code).

### S4 /delete removes the session's topic

Anchors: `task_commands.go` `deleteCommand`; `DeleteForumTopic`.

Tests (red first):
- `TestDeleteInForumGroupDeletesTopic`: deleteForumTopic called; a
  confirmation addressed into the deleted topic is rerouted to the group root.

### S5 Rebind collision guard

Tests:
- `TestRebindTaskTopicCollision`: refuses a sibling-owned topic; free topic
  rebind resolves.

## Red-team checklist

- Non-forum group / missing manage_topics -> create fails, old behavior.
- /new inside a bound topic in a group: forum path mints a fresh topic; the
  fallback path reports the binding conflict instead of stealing it.
- Forum API failure after task creation leaves a working unbound session,
  never a failed command with no task.

## Acceptance

Unit + `just check` green; `just docker-check` (image-side change). Live:
create a forum group, add the bot, grant manage_topics, `/new` -> new topic.
Live evidence requires the owner's bot and group — recorded on the issue.

## Out of scope

- `/archive`/`/unarchive` and close/reopenForumTopic wiring: issue #217.
- Rename command surface (no `/rename` exists); renaming happens via
  auto-title only.
