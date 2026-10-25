---
description: Telegram bot channel — adapter contract, file/usage/voice surfaces, QR auth page, limits.
last_verified: 2026-10-25
---

# Channel: Telegram (bot API)

The production channel. Outbound polling only — no public ingress; the bot
token lives solely in communication-hub env. Contract: internal/channel
`NewTelegram(TelegramDeps)` (docs/channels.md); adapter id `telegram_bot`.

## Identity and admission

- `TELEGRAM_ALLOWED_USERS` (infra owner) or `communication.users.yaml`
  (`HUB_COMMUNICATION_CONFIG`) is the sender allowlist; a non-allowlisted
  chat gets the not-configured reply. Telegram user/chat IDs are transport
  links — never filesystem or authorization keys.
- Forum/topic routing: `chat_id + message_thread_id` select the delivery
  audience; inline approvals retain topic routing.
- Groups are disabled by default; private allowlisted DMs are the supported
  shape.

## Inbound files

A Telegram file job carries a trusted staged ID separate from user text.
The runtime resolves it through the owner's `inbox_fetch` ToolHub grant
*before* admitting the run — a failed download/save never starts a
fileless run. Extraction/receipt semantics: docs/tools.md §Contract notes.

## Owner commands

Deterministic, no model wake: `/usage [day|week|month] [model=<id>]` reads
the version-2 usage journal (deltas of cumulative session counters keyed by
scope/context/session; v1 legacy totals counted separately; a reset or
missing counter reads `unknown` — no invented prices/durations) plus
configured provider quota. `/usage session` queries the live session —
explicit, two-query limit, may wake its runtime. `/status`, `/routines`,
`/watches`, `/connectors`, `/voice` are deterministic reads. `/compact` and
`/compress` run one bounded session compaction through `POST /v1/compact`
(here/focus/prune/preview args; busy sessions refuse or queue). `/goal
<text>|status|pause|resume|clear` manages the persistent per-conversation
goal through `POST /v1/goal`; set queues a kickoff turn. Kickoff, resume and
continuations retain the originating sender, chat and topic, including the
dedicated group scope outside topics mode. Only goal turns from the current
epoch reach the judge; ordinary messages preempt pending turns without being
judged, and `/goal resume` restarts the loop. Group authorization denials at
dispatch log the job ID, channel and a fixed reason without message content.
`/credentials`,
`/confirm`, `/remove`, `/connect`, `/device` mint or render the `?t=`
human-action pages.

## Outbound

Replies render Telegram HTML, split on block boundaries inside the channel
limit with durable per-part outbox progress (SPEC-0036). Files generated
under `workspace/artifacts/` cross through `POST /v1/artifact` while the
runtime answers, stage into the outbox, and arrive as photos/documents/
videos with per-artifact progress (SPEC-0037). `MEDIA:`/`VOICE:` reply
markers: docs/tools.md. `ErrUncertain` records uncertainty instead of a
duplicate-retry; cancellation cannot recall a Telegram-accepted message.
Unknown run outcomes are observed without replaying input. After the recovery
budget expires and its one-time uncertainty notice is durable in the outbox,
automatic observation stops; the outcome stays unknown, including when the
notice's delivery itself is uncertain.

## QR login page (telegram_auth, compiled in)

MTProto QR login is compiled into the standard gateway (CHG-0137); it is
default-on for stacks with `ingress: [telegram]` unless `telegram_auth:
false`.

On `ingress: [telegram]` the gateway serves a loopback QR page for
`intake: telegram_qr` broker contracts: Go MTProto in-process, owner scans
in Telegram Devices, two-step password handled on the page, a
Telethon-compatible StringSession is created in memory and submitted
through an owner-signed Telegram-only broker request — never written to
spool, logs or files. `telegram_auth: false` disables the page; app API
pair via `spaces/<user>/telegram-auth.<env>.env`
(`HUB_TELEGRAM_AUTH_API_ID/HASH`). Details: docs/prepared-connectors.md
§personal-telegram.

## Mock adapter

`channel.NewMock` is the test/canary stand — file-backed, owner-scoped
client IDs, byte-captured attachments, never a real send.
