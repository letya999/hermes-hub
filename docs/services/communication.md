---
description: communication-hub — channel gateway: Telegram/Slack adapters, inbound routing, credential forms, spool, control API.
last_verified: 2026-10-29
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
total logical owner storage), `HUB_SPOOL_MAX_DELIVERIES` (1024 ordinary pending
outbox entries; control lane exempt).

Storage accounting covers the spool tree, including outbox blobs, archive,
recovery records and scratch, plus configured `mock-sent`/`mock-links` directories.
`HUB_SPOOL_MAX_OWNED_FILES` bounds filesystem entries (default 65536, including
subdirectories); `HUB_SPOOL_MAX_NEXT_WRITE_BYTES` bounds a single write and
admission headroom (64 MiB). `HUB_SPOOL_LIFECYCLE_RESERVE_BYTES` (32 MiB) and
`HUB_SPOOL_LIFECYCLE_RESERVE_FILES` (128) reserve completion/control capacity.
These five disk limits must be positive; next-write plus reserve must fit the
byte ceiling and the entry reserve must be smaller than the entry ceiling.
Counters reconstruct on open, reconcile after uncertain writes and every
30 seconds on mutation; ordinary metadata reads do not walk the tree.
Logical bytes are not physical allocated bytes or free-volume space. Archive
replacement requires scratch capacity before source deletion; a full owner
stops new admission rather than deleting protected state. Operator maintenance
and cross-owner status remain in progress under CHG-0199.


Retention and storage bounds (hourly compaction, or after five minutes at 80% of the byte/file limit or following a cleanup failure): `HUB_SPOOL_RETENTION`
(default 72h) ages terminal records out of `done/`, `failed/`,
`outbox/{done,failed}/`, settled `run-events/` and terminal `mappings/` into
verified, immutable gzip chunks under `archive/` — history compresses instead of disappearing.
`HUB_SPOOL_MAX_FILES` (default 2048) caps each terminal dir's record count
inside the window; `HUB_SPOOL_MAX_ARCHIVE_BYTES` (default 64 MiB) evicts the
oldest chunks. `usage.jsonl` is rewritten keeping the retention window plus
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

Event layout v2 separates `event-state/` (subscription cursors/seen sets),
`run-events/` (runtime receipts) and `provider-dedup/` (provider replay markers).
Startup migrates legacy `events/` under the spool writer fence: a durable
manifest records canonical identity and byte digests before moves; conflicting,
unknown or unsafe records cause refusal. Restart resumes the same moves. A final
`events` format file prevents older writers from recreating the mixed directory.
Initialized cursor loss is an explicit degraded gap/recovery error, never a new
silent baseline. Cursor state and provider dedup are outside terminal compaction.
An unfinished run or approval keeps its receipt even without a pending delivery.

Archive chunks use stable content digests across retry/day boundaries. Gzip
verification, file sync and directory sync precede source removal; partial
`.archive-*` scratch is never treated as a committed chunk. Windows directory
sync remains the existing no-op; Linux filesystem acceptance is required for
durability claims. Layout/retention evidence is tracked in CHG-0192.

## Storage reliability handoff (CHG-0198, acceptance pending)

Worker completion, failure and uncertain outcomes use the existing `run-events/`
receipt for outcome plus exact delivery intent before updating mapping, outbox
and source job state. A failed mandatory save blocks successful acknowledgement.
Recovery replays local derivatives without resubmitting the prompt. Streamed
voice and artifact metadata stay in the canonical receipt; mapping alone is not
a complete result. A worker requires a matching durable admission before calling
the runner; observation clears executable input while retaining its prompt hash.

Each `event-state/` cursor can retain one unfinished source batch: owner/source
binding, schedule and probe revisions, cursor interval, exact event IDs, filter
verdicts, deterministic sink IDs and prepared delivery/job payloads. Intent is
written before fanout. The existing bounded loops drain a saved remainder before
another fetch, without rerunning its filter. Cursor progress follows durable
sink admissions or drop decisions. Batch decisions remain valid when the
512-entry seen window evicts an older ID. Agent bundles remain bounded at 8 KiB
of event text and one admitted bundle per pass; notify fanout admits at most eight
per pass. The batch is capped at 1024 events and the whole cursor record at 4 MiB;
oversized or corrupt records block rather than advancing a cursor.

Agent batch references bind schedule and probe revisions. Claim and delivery
check current owner, pause, assignment generation and operational epoch fences.
Unfinished batches remain durable across sink-capacity failures and restarts.
A consuming source without ACK/idempotency can still lose a result between its
remote consume and our first durable intent; local replay does not establish
provider exactly-once. Use non-consuming cursor sources where available.
A terminal failure or an absent tool event does not prove an effect-free run.
Worker failures keep the original job identity; they do not create `-r1` work.
The runtime likewise does not re-admit an observed failure under a `:wake` key.
Submission retry uses the same job/idempotency identity and is limited to a
connection failure before dispatch or a trusted supervisor 503 carrying
`X-Hub-Admission: not-dispatched`. Unmarked 503, unbound server errors and invalid
post-dispatch replies remain uncertain. Unknown delivery is reconciled rather
than blindly sent again. SQLCipher remains gated to Broker; this spool stays JSON.
Isolated native/container acceptance remains outstanding; local protocol and
filesystem tests do not certify the full milestone.

Assignment wakes reserve their logical job ID and applied generation in the
existing assignment checkpoint before enqueue. An enqueue failure or owner-day
rollover cannot charge that same wake twice; pause/generation checks still run
before reuse. The reservation is released only after the job mapping is durable.
Unsettled wake reservations are capped at 1024 per assignment; capacity stops
new reservation rather than deleting an unresolved operation. Status-receipt
recovery uses the same editable-status lifecycle and does not resurrect failed
edits. Sink-admission failures stay visible and propagate through the proactive
pass instead of being recorded as successful settlement.

Editable elapsed-time updates carry the current event position and a text-bound
edit identity. New elapsed text at the same position can update the message;
an exact recovered coalesced edit stays fenced by its sending/done/failed record.

An uncertain observation binds its canonical receipt to the original run/session
and the observed runtime generation. Trusted observation rebind can record a new
uncertain generation; recovery skips old-generation receipts without changing the
source scope. Non-event watcher escalation also saves its exact job in the
existing `probe-result` receipt before reserving a wake or enqueueing. Recovery
checks the original occurrence, owner, watch identity and delivery audience;
replaying another source's receipt is an error. Exact unmapped pending admission
can be repaired, while missing run/terminal admission evidence stays blocked.
