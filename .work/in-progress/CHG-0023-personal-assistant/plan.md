# CHG-0023: Personal Assistant Experience (M4)

## Objective

Deliver the everyday personal-assistant path on the existing communication-hub,
supervisor and scoped homes. Close GitHub issues #29–#36 and milestone 5.
Do not rewrite closed M0–M3 contracts.

## Scope

1. Harden Telegram Bot API as ingress/delivery only: verified numeric sender
   to one principal, commands, files, edits, idempotent delivery, groups off.
2. Slack App Events API as a separate communication channel with official
   `v0` HMAC verification. No Slack data tools. No email/display-name linking.
3. Voice envelope + STT; opt-in TTS with text as the canonical delivery.
4. Hub-owned routine CRUD, occurrence jobs, no dual native cron, live wake.
5. Native Hermes memory/profile isolation, optional official Honcho config,
   skills without image rebuild, context backup/restore/export/purge.

## Explicitly deferred

M5/M6/M7/M8, live ToolHive/VPS (#73), Hermes reconnect (#74), Slack data
connector, personal Telegram MCP, group-chat disclosure, live account login
or sending without a separate owner command.

## Verification

- Shipped-path Go tests for handleUpdate, Slack signature+events, STT/TTS,
  routine CRUD/dispatch, memory/skills isolation, hubctl backup/restore/purge
- `just check` with own Go statement coverage >=85%
- `just security` only if module/lockfiles change
- `just docker-check` when Docker is available; fixture vs live labeled
