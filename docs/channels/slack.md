---
description: Slack App channel — Events API + Socket Mode ingress, workspace mapping, limits.
last_verified: 2026-11-16
---

# Channel: Slack (Events API / Socket Mode)

Supported, not production-primary. `SLACK_TRANSPORT` picks the ingress per
deployment: `events` (default) keeps the public webhook, `socket` opens an
outbound WebSocket so a stack fully inside a VPN perimeter works with no
inbound listener.

## Transports

- `events` — Slack POSTs to `/v1/slack/events`; official `v0` HMAC
  verification on every request; the `slack_app` feature moves the
  communication-hub listener to the Slack events port and requires HTTPS
  ingress in front. Unchanged default.
- `socket` — the gateway POSTs `apps.connections.open` with
  `SLACK_APP_TOKEN` (xapp-…), dials the returned `wss://` endpoint, and acks
  each `envelope_id` only after the payload is durably ingested — the same
  at-least-once contract the HTTP route keeps with its status code. No
  inbound port is needed, so the events route is not mounted in this mode.
  `events_api` frames reuse the identical `ingestSlackEvent` normalization;
  `slash_commands` frames run `/command` through the same dispatcher with
  the IM-only gate (a slash typed in a C…/G… channel is denied like an
  Events channel message). Replayed envelopes dedupe on the durable
  event/envelope marker, not on the retry.
- Both modes deliver replies through `chat.postMessage` with
  `SLACK_BOT_TOKEN`; socket mode adds `SLACK_APP_TOKEN` and the gateway
  never reads `SLACK_SIGNING_SECRET` — app-level auth replaces HMAC
  (the stack scaffold still lists the secret for the events path).
- Reconnects are bounded (fixed delay, named `slack-socket-egress` log
  condition once per transition).

## Env

`SLACK_BOT_TOKEN`, `SLACK_SIGNING_SECRET`, `SLACK_APP_TOKEN`,
`SLACK_TRANSPORT` stay in communication-hub env — channel credentials only;
they never enable Slack *data* tools (that's the separate
`korotovsky/slack-mcp-server` OAuth path, docs/integrations.md). Slack App
creds and Slack user-token creds are different identity planes. The app
token is referenced, never logged or surfaced in status.

## Corporate egress proxy

Export `HUB_EGRESS_PROXY` (an `http(s)` proxy URL, credentials allowed)
before `hubctl up` and every Slack call CONNECTs through it — Web API,
`apps.connections.open`, and the Socket Mode WebSocket handshake all ride
the same transport. The destination allowlist stays host-side. Malformed
values fail startup closed; an unreachable proxy logs a named
`slack-socket-egress`/`telegram-egress` condition once per transition.
Enumerable egress destinations for the firewall team: `slack.com` (Web API
+ `apps.connections.open`) and the `wss-*.slack.com` Socket Mode endpoints.

## Limits

Events-mode ingress: `/`-prefixed IM text runs through the shared command
dispatcher (`dispatchChannelCommand`); socket mode additionally routes real
Slack slash-command invocations through the same path. Interactive payloads
(buttons/shortcuts) are acked but not wired; thread/message timestamps
validated; no username/icon impersonation. Live workspace acceptance is an
explicit gate — HMAC and loopback-socket fixtures do not prove it.
