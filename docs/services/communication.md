---
description: communication-hub — channel gateway: Telegram/Slack adapters, inbound routing, credential forms, spool, control API.
last_verified: 2026-11-19
---

# communication-hub

`cmd/communication` → `internal/communication`. The channel gateway that owns
everything entering or leaving the human conversation. Listens
`HUB_COMMUNICATION_LISTEN` (`0.0.0.0:8081`), host-bound to
`127.0.0.1:<host-port>` — 8081 normally, the Slack events port when
`slack_app` is enabled (docs/channels/slack.md).

Channel authority is `contracts/identity/v1` in control. `HUB_IDENTITY_URL` and
`HUB_IDENTITY_AUTH` select it; supervised stacks default to the supervisor
URL/bearer. Legacy channel lists are explicit migration inputs, while user
paths/passport metadata remain routing ceilings. Unknown, revoked or unavailable
registry identities deny without a static fallback, including delayed delivery
and owner notices. OIDC proof/unlink calls require separate
`HUB_IDENTITY_LINK_AUTH`; the gateway retains only transient one-time login
challenges, not the old IAM link writer. See [identity-hub](identity-hub.md).

## Responsibilities

- **Channel adapters** (`internal/channel`): Telegram and Slack — registry +
  conformance tests, contract in ADR-0047. Per-adapter docs in
  docs/channels/.
- **Inbound**: normalizes transport identity into `identity.Envelope`
  (docs/rbac.md), maps conversations to runtimes, delivers to the runtime
  HTTP surface or supervisor.
  Telegram document/photo intake accepts up to 20 MiB per file; model vision
  uses bounded derivatives rather than forwarding full source bytes.
- **Outbound delivery**: model replies render to Telegram HTML parts
  (~4,000 UTF-16 units each). A response beyond five parts degrades to a
  localized truncation notice plus the final part — where the conclusion
  lands — with the full source attached as `answer.md`; a pathological
  reply can never flood the chat.
- **Credential forms**: mints the protected links (`?t=`) used by grant
  confirmations, connector onboarding (`channel_auth_url` outranks the
  broker form), writes-enable and remove nonces. `HUB_COMMUNICATION_FORM_ORIGIN`
  fixes the host-facing origin.
- **Control API** on the same listener: `prepare-outcome` callbacks, routines
  control, sibling-runtime calls authenticated by `toolhub-tokens.json`
  (`HUB_COMMUNICATION_TOKENS_FILE`). In supervised stacks with distinct service
  authority, the primary runtime bearer is pinned to rendered `HUB_USER_ID`;
  sibling bearers are pinned to enrollment. Request headers cannot switch them
  to another principal; an unknown primary owner denies access.
- **Human confirmations**: service-issued notices and verified owner taps use
  the automation service bearer, including the return call to ToolHub. Runtime
  bearers cannot submit confirmation notices. Callback routing comes from the
  verified channel; profile, environment, generation and organization remain
  bound to the issued proof and are rechecked by ToolHub before applying it.
- **Automation delivery intake**: `POST /v1/automation/deliveries` and
  `GET /v1/automation/owner-home` accept the automation service bearer
  (`HUB_AUTOMATION_AUTH`, falling back to `HUB_COMMUNICATION_AUTH` only when
  unset) plus `X-Hub-Principal`; deliveries additionally require
  `X-Hub-Context`. Supervised stacks use the supervisor bearer;
  owner and sibling runtime bearers cannot read arbitrary owner homes or
  submit service deliveries.
  The handler pins the owner to a current enabled user and verifies the
  Telegram/mock chat or Slack identity against the channel target,
  then writes the stable delivery ID to the existing outbox. The owner scope
  is stamped server-side from the verified headers — the body can never
  claim it — and `automation` marks the record so the send worker rechecks
  owner, the live `channels.send` right and the service wiring before send.
  ToolHub owns the clock and durable automation state; communication owns
  the channel handoff.
- **Master hold notices**: the control loop syncs supervisor `/v1/holds` on
  boot and every second while a notice is pending; a clean hold set backs off
  to a ~10 s poll. A routable notice is durably enqueued (Telegram) then
  acknowledged via `/v1/holds/ack`. A notice with no configured delivery
  channel is acknowledged immediately — undeliverable notices never wedge
  pending.
- **Spool**: durable outbound queue on the `communication-hub-data` volume
  (`HUB_COMMUNICATION_SPOOL=/data`). Every admitted job holds exactly one
  reservation in `budget.json` (written before the job file, settled once at
  terminal outcome; unmeasured usage settles at the estimate and counts as
  `unknown`). Fair claim rotates by scope and promotes background jobs parked
  past the 5-minute aging bound. Control deliveries (approvals, marked
  control sends) claim ahead of ordinary replies and are exempt from the
  ordinary outbox cap.
- **Memory capture outbox** (`capture_outbox.go`): accepted jobs and completed
  run receipts mint durable `capture/` records before their transport handoff
  completes. The pump posts them to ToolHub with the communication service
  bearer and the enrolled principal; a lost acknowledgement or outage leaves
  the event pending. Settlement is recorded under `capture-acks/` before the
  outbox copy is removed; settled markers age out only after their jobs.
  Startup rebuilds missing events from jobs and
  terminal receipts created with capture enabled. Communication has no vault
  mount or nightly memory clock; ToolHub owns capture and nightly writes.

## Key env

`HUB_USER_ID`, `HUB_ORGANIZATION_ID`, `HUB_RUNTIME_ID`, `HUB_POLICY_VERSION`
(envelope identity), `HUB_FEATURES` (feature list drives which adapters boot),
`HUB_RUNTIME_URL` / `HUB_RUNTIME_SUPERVISOR_URL` + `HUB_SUPERVISOR_AUTH`
(delivery targets), `HUB_TOOLHUB_URL` (`toolhub:8090` direct or
`toolhub-control:8090` via supervisor), `HUB_TELEGRAM_AUTH_ENABLED`,
`HUB_COMMUNICATION_CONFIG` (`communication.users.yaml` — sibling Telegram
identities; `TELEGRAM_ALLOWED_USERS` is the bot-level allowlist and must
contain every owner's numeric id),
`HUB_MODEL_RELAY_ADMINS_FILE` (enrolled
`model-relay-admins.json`, principal → relay URL + admin bearer) for the
shared gateway serving several managed principals — `/model` resolves the
caller's relay from the authenticated identity. Slash commands share one
dispatcher across channels: Telegram updates and `/v1/mock/message` texts
starting with `/` both run it, so mock replies land in `mock-sent` and
never enqueue an agent job,
`HUB_STT_URL`/`HUB_TTS_URL` + `media.auth` when the space renders a voice
channel (`config.channels.*.voice` in the published revision, or the legacy
`transcription` feature),
`HUB_CREDENTIAL_BROKER_APPROVE_*` (broker client for approvals).

Startup requires `HUB_RUNTIME_SUPERVISOR_URL` or `HUB_RUNTIME_URL`; without
either endpoint the gateway exits with a localized error before opening its
spool. It never starts Hermes locally or copies provider env into user records.
The HTTP job contract does not carry `HUB_USER_LANG`; removing the local
runner does not change that existing language propagation limitation.

Protected ToolHub confirmation notices require the service bearer and an
owner-bound actor. The signed actor's current policy may differ from the
gateway's loaded sibling routing snapshot; the final callback still goes to
ToolHub with that actor for current-policy admission. A stale routing policy
must be refreshed for new channel jobs, but it cannot suppress the owner's
already-issued confirmation button.

`GET /v1/prepare-delivery-status` is service-bearer-only. With an exact
onboarding ID and notice version, it reads the durable outbox record matching
the caller's principal/context/runtime identity and returns only `unknown`,
`queued`, `uncertain` or `delivered`. It does not return links, chat content or
credentials. ToolHub maps a notice not yet accepted by this gateway to
`not_queued` before calling the endpoint.

Admission ceilings (host-only, read at spool open): `HUB_SPOOL_MAX_PENDING_PER_SCOPE`
(default 16), `HUB_SPOOL_MAX_PENDING_TOTAL` (128), `HUB_SCOPE_TOKEN_DAILY`
(0 = unsettled+reserved token ceiling off), `HUB_SPOOL_MAX_BYTES` (256 MiB
pending-queue storage), `HUB_SPOOL_MAX_DELIVERIES` (1024 ordinary pending
outbox entries; control lane exempt).

Retention and storage bounds (hourly compaction): `HUB_SPOOL_RETENTION`
(default 72h) ages terminal records out of `done/`, `failed/`,
`outbox/{done,failed}/`, `events/` and terminal `mappings/` into daily
gzip bundles under `archive/` — history compresses instead of disappearing.
`HUB_SPOOL_MAX_FILES` (default 2048) caps each terminal dir's record count
inside the window; `HUB_SPOOL_MAX_ARCHIVE_BYTES` (default 64 MiB) evicts the
oldest bundles. `usage.jsonl` is rewritten keeping the retention window plus
the newest record per session (the delta baseline a live session needs);
`HUB_SPOOL_MAX_USAGE_ENTRIES` (default 65536) bounds it, oldest non-baseline
records leaving first. Uncertain `failed/` jobs awaiting re-observation,
receipts for in-flight deliveries, live job mappings and pending/running
work are never compacted.

## Boundaries

- Host port is localhost-only; Telegram is outbound polling (no public
  ingress), Slack events is the one inbound surface requiring exposure.
- The form origin is the only URL the owner is told to open; links carry
  single-use nonces bound to the record digest.
- Owns `?t=` links; never holds credential values — those terminate at the
  broker.
