---
description: communication-hub — channel gateway: Telegram/Slack adapters, inbound routing, credential forms, spool, control API.
last_verified: 2026-10-24
---

# communication-hub

`cmd/communication` → `internal/communication`. The channel gateway that owns
everything entering or leaving the human conversation. Listens
`HUB_COMMUNICATION_LISTEN` (`0.0.0.0:8081`), host-bound to
`127.0.0.1:<host-port>` — 8081 normally, the Slack events port when
`slack_app` is enabled (docs/channels/slack.md).

## Responsibilities

- **Channel adapters** (`internal/channel`): Telegram and Slack — registry +
  conformance tests, contract in ADR-0047. Per-adapter docs in
  docs/channels/.
- **Inbound**: normalizes transport identity into `identity.Envelope`
  (docs/rbac.md), maps conversations to runtimes, delivers to the runtime
  HTTP surface or supervisor.
- **Credential forms**: mints the protected links (`?t=`) used by grant
  confirmations, connector onboarding (`channel_auth_url` outranks the
  broker form), writes-enable and remove nonces. `HUB_COMMUNICATION_FORM_ORIGIN`
  fixes the host-facing origin.
- **Control API** on the same listener: `prepare-outcome` callbacks, routines
  control, sibling-runtime calls authenticated by `toolhub-tokens.json`
  (`HUB_COMMUNICATION_TOKENS_FILE`).
- **Spool**: durable outbound queue on the `communication-hub-data` volume
  (`HUB_COMMUNICATION_SPOOL=/data`).

## Key env

`HUB_USER_ID`, `HUB_ORGANIZATION_ID`, `HUB_RUNTIME_ID`, `HUB_POLICY_VERSION`
(envelope identity), `HUB_FEATURES` (feature list drives which adapters boot),
`HUB_RUNTIME_URL` / `HUB_RUNTIME_SUPERVISOR_URL` + `HUB_SUPERVISOR_AUTH`
(delivery targets), `HUB_TOOLHUB_URL` (`toolhub:8090` direct or
`toolhub-control:8090` via supervisor), `HUB_TELEGRAM_AUTH_ENABLED`,
`HUB_COMMUNICATION_CONFIG` (`communication.users.yaml` — sibling Telegram
identities; `TELEGRAM_ALLOWED_USERS` is the bot-level allowlist and must
contain every owner's numeric id),
`HUB_MODEL_RELAY_URL` + `model-relay.auth` only when the gateway itself is
rendered inside a managed space (shared comm-hub serving managed principals
has no per-principal relay — `/model` is unavailable there),
`HUB_STT_URL`/`HUB_TTS_URL` + `media.auth` when `transcription` is on,
`HUB_CREDENTIAL_BROKER_APPROVE_*` (broker client for approvals).

## Boundaries

- Host port is localhost-only; Telegram is outbound polling (no public
  ingress), Slack events is the one inbound surface requiring exposure.
- The form origin is the only URL the owner is told to open; links carry
  single-use nonces bound to the record digest.
- Owns `?t=` links; never holds credential values — those terminate at the
  broker.
