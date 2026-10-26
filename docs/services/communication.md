---
description: communication-hub — channel gateway: Telegram/Slack adapters, inbound routing, credential forms, spool, control API.
last_verified: 2026-10-26
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
- **Master hold notices**: polls supervisor `/v1/holds` once per control tick;
  a routable notice is durably enqueued (Telegram) then acknowledged via
  `/v1/holds/ack`. A notice with no configured delivery channel is
  acknowledged immediately — undeliverable notices never wedge pending.
- **Spool**: durable outbound queue on the `communication-hub-data` volume
  (`HUB_COMMUNICATION_SPOOL=/data`). Every admitted job holds exactly one
  reservation in `budget.json` (written before the job file, settled once at
  terminal outcome; unmeasured usage settles at the estimate and counts as
  `unknown`). Fair claim rotates by scope and promotes background jobs parked
  past the 5-minute aging bound. Control deliveries (approvals, marked
  control sends) claim ahead of ordinary replies and are exempt from the
  ordinary outbox cap.

## Key env

`HUB_USER_ID`, `HUB_ORGANIZATION_ID`, `HUB_RUNTIME_ID`, `HUB_POLICY_VERSION`
(envelope identity), `HUB_FEATURES` (feature list drives which adapters boot),
`HUB_RUNTIME_URL` / `HUB_RUNTIME_SUPERVISOR_URL` + `HUB_SUPERVISOR_AUTH`
(delivery targets), `HUB_TOOLHUB_URL` (`toolhub:8090` direct or
`toolhub-control:8090` via supervisor), `HUB_TELEGRAM_AUTH_ENABLED`,
`HUB_COMMUNICATION_CONFIG` (`communication.users.yaml` — sibling Telegram
identities; `TELEGRAM_ALLOWED_USERS` is the infra-owner fallback),
`HUB_MODEL_RELAY_URL` + `model-relay.auth` for a gateway rendered inside its
own managed space, or `HUB_MODEL_RELAY_ADMINS_FILE` (enrolled
`model-relay-admins.json`, principal → relay URL + admin bearer) for the
shared gateway serving several managed principals — `/model` resolves the
caller's relay from the authenticated identity. Slash commands share one
dispatcher across channels: Telegram updates and `/v1/mock/message` texts
starting with `/` both run it, so mock replies land in `mock-sent` and
never enqueue an agent job,
`HUB_STT_URL`/`HUB_TTS_URL` + `media.auth` when `transcription` is on,
`HUB_CREDENTIAL_BROKER_APPROVE_*` (broker client for approvals).

Admission ceilings (host-only, read at spool open): `HUB_SPOOL_MAX_PENDING_PER_SCOPE`
(default 16), `HUB_SPOOL_MAX_PENDING_TOTAL` (128), `HUB_SCOPE_TOKEN_DAILY`
(0 = unsettled+reserved token ceiling off), `HUB_SPOOL_MAX_BYTES` (256 MiB
pending-queue storage), `HUB_SPOOL_MAX_DELIVERIES` (1024 ordinary pending
outbox entries; control lane exempt).

## Boundaries

- Host port is localhost-only; Telegram is outbound polling (no public
  ingress), Slack events is the one inbound surface requiring exposure.
- The form origin is the only URL the owner is told to open; links carry
  single-use nonces bound to the record digest.
- Owns `?t=` links; never holds credential values — those terminate at the
  broker.
