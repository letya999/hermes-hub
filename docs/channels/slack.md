---
description: Slack App channel — Events API ingress, HMAC verification, workspace mapping, limits.
last_verified: 2026-10-30
---

# Channel: Slack (Events API)

Supported, not production-primary. The only channel with a public inbound
surface: `slack_app` feature moves the communication-hub listener to the
Slack events port and requires HTTPS ingress in front.

## Contract

`channel.NewSlack(SlackDeps)` — official `v0` HMAC request verification on
every event; workspace + sender map to the configured user scope through
the same `JobEnvelope` seam as Telegram. Delivery renders mrkdwn, same
outbox/spool/delivery mechanics — adapters only translate.

## Env

`SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `SLACK_APP_TOKEN` stay in
communication-hub env — channel credentials only; they never enable Slack
*data* tools (that's the separate `korotovsky/slack-mcp-server` OAuth path,
docs/integrations.md). Slack App creds and Slack user-token creds are
different identity planes.

## Limits

Events-only ingress: `/`-prefixed IM text runs through the shared command
dispatcher (`dispatchChannelCommand`) so `/language`, `/new`, `/compact` and
friends answer on the originating channel/thread, but Slack slash-command and
shortcut payloads are not wired; thread/message timestamps validated; no
username/icon impersonation. Live workspace acceptance is an explicit gate —
HMAC fixtures do not prove it.
