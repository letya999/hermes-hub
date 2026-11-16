# CHG-0311: corporate egress proxy + Slack Socket Mode (M9)

Owner instruction: one CHG, two commits — (1) `HUB_EGRESS_PROXY` +
`egressTransport` for telegram/slack/oauth clients (#353), (2)
`SLACK_TRANSPORT=socket` via `SLACK_APP_TOKEN` and `coder/websocket` (#350),
events stays the default. Milestone
https://github.com/letya999/hermes-hub/milestone/7 (MVP M9 — Enterprise
Readiness).

## Goal

A deployment inside a corporate VPN perimeter keeps working outbound-only:
the gateway's own egress (Telegram long-poll, Slack API, Slack Socket Mode,
OAuth token/discovery calls inside toolhub's `oauth.Broker`) honors one
operator-configured HTTP proxy, and the Slack channel can ingest without any
public ingress.

## Slices

### S1 #353 — `HUB_EGRESS_PROXY` + `egressTransport`

- `internal/netguard/egress.go`: `EgressProxy()` parses
  `HUB_EGRESS_PROXY` (empty → nil; non-http(s)/hostless → error, fail closed —
  URL userinfo stays legal: corp proxies take basic auth); `EgressTransport()` clones `http.DefaultTransport` and
  pins `Proxy: http.ProxyURL` — explicit config beats ambient `HTTP(S)_PROXY`;
  malformed value yields an `errTransport` that fails every call (the floor
  under config validation). `netguard.Client` prefers the egress transport
  when configured so `oauth.Broker` rides the proxy with zero call-site
  change; the local dial guard is the proxy's job in that mode (the corp
  allowlist lives host-side).
- `internal/communication/telegram_api.go` + `slack.go`: both `new*API`
  clients get `Transport: netguard.EgressTransport()`.
- `internal/communication/config.go`: `Config.Validate` calls
  `netguard.EgressProxy()` — a malformed value refuses startup.
- `internal/communication/gateway.go`: poll loop splits API-answered errors
  (`telegramStatusError`) from transport errors; the latter flip a named
  `<channel>-egress` log condition once per transition, recovery logs once —
  "unreachable egress is a named condition, not silent retry".
- `internal/stack/compose.go`: `HUB_EGRESS_PROXY` renders as
  `${HUB_EGRESS_PROXY:-}` into communication-hub and toolhub environments —
  same operator-env channel as `HUB_RUNTIME_SUPERVISOR_URL`.
- Tests: `netguard` CONNECT-proxy round trip + malformed fail-closed +
  `Client` honors the env; telegram client through a CONNECT proxy fixture.
- Docs: `docs/channels/telegram.md`, `docs/channels/slack.md` env sections —
  proxy knob + enumerable egress hosts for the firewall team.

### S2 #350 — `SLACK_TRANSPORT=socket`

- `internal/communication/config.go`: `SlackTransport` (`""`/`events`/
  `socket`; `socket` requires `SLACK_APP_TOKEN`), `SlackAppToken` from env.
- `internal/communication/slack.go`: envelope parsing lifted to
  `slackEventEnvelope` + `dispatchSlackEvent` shared by the HTTP handler and
  the socket loop.
- `internal/communication/slack_socket.go`: `apps.connections.open` with the
  xapp token → `websocket.Dial` via the egress client (`SetReadLimit` 1 MiB),
  per-envelope `{"envelope_id"}` ack, `events_api` payload →
  `dispatchSlackEvent` (same dedup + ingest), `slash_commands` payload →
  normalized `/cmd args` text into `ingestSlackEvent` (im-scope only, dedup on
  `slash-<trigger_id>`), `disconnect` → reconnect; bounded backoff on failure,
  same named `slack-socket-egress` condition.
- `internal/communication/gateway.go`: socket loop starts beside the other
  loops only when `SlackTransport == "socket"`; the `/v1/slack/events` route
  stays registered either way.
- `internal/stack/compose.go`: `SLACK_TRANSPORT` renders as
  `${SLACK_TRANSPORT:-events}` into communication-hub. `SLACK_APP_TOKEN`
  already flows secrets → `communication.<env>.env`.
- `go.mod`: `coder/websocket` moves from indirect to direct (already pinned
  v1.8.15 — no new dependency or version).
- Tests: fixture `websocket.Accept` server — envelope → ack + durable ingest +
  dedup; slash command → command route; events mode untouched.
- Docs: `docs/channels/slack.md` transport section (socket vs events
  trade-off), `last_verified` bump on touched docs.

## Red-team checklist

- Malformed `HUB_EGRESS_PROXY` → startup error / failing transport, never
  silent direct egress.
- `SLACK_TRANSPORT=socket` without `SLACK_APP_TOKEN` → config error.
- App token never logged/audited (socket loop logs conditions, not URLs —
  the wss ticket is a credential).
- Replay: socket `events_api` redelivery dedups on `event_id`; slash
  redelivery dedups on `trigger_id`.
- Envelope flood: read limit + per-envelope ack before ingest; ingest errors
  can't kill the loop (poison envelope → log + continue, mirroring the HTTP
  handler's reject path).

## Acceptance

- Offline: focused package tests green; `go vet`; `just check`.
- Live (deferred to merge session): dev stack with `HUB_EGRESS_PROXY` pointed
  at a throwaway CONNECT proxy — telegram poll + slack socket work with zero
  direct egress; events transport unchanged without the env.

## Out of scope

- Per-destination allowlists inside the hub (the corp proxy is the boundary).
- Proxy auth rotation, per-channel proxy overrides, SOCKS.
- Interactive/block-kit socket payloads, slash commands outside im scope
  (same denial as events mode).
- `webhook`/public-ingress changes; `events` remains the default transport.
