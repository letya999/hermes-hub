---
description: Current M10 file, routine, mock, knowledge and usage contracts after stabilization.
last_verified: 2026-10-12
---
# M10 stabilization

This document supplements the frozen [file transfer](../specs/active/SPEC-0047-chat-file-transfer.md),
[routines](../specs/active/SPEC-0048-proactive-routines.md) and
[operability](../specs/active/SPEC-0049-operability.md) requirements.
See [SPEC-0050](../specs/active/SPEC-0050-m10-stabilization.md) and
[ADR-0033](adr/ADR-0033-deterministic-work-isolation.md).

## Files and images (#150, #151)

Delivery copies a file; it never deletes the workspace original. Legacy artifact
`consume` requests are accepted but no longer remove originals. The user removes
artifacts explicitly. An incoming transfer lands in `inbox/<id>/files/`; ZIPs
extract there and a receipt is stored in `inbox/<id>/receipt.json` before the
staged copy is acknowledged. Retry reuses the receipt, retries acknowledgment and
never downloads/extracts again. `acknowledged=false` means local files exist but
the staged copy still needs acknowledgment. A pre-existing directory without a
valid receipt requires operator inspection; it is not overwritten or deleted.

Telegram file jobs carry a trusted staged ID separately from user text. Before
admitting the agent run, the runtime saves the file through the owner's ToolHub
`inbox_fetch` grant and gives the resulting workspace path to the agent. Failed
downloads or saves do not start fileless runs. PDF extraction uses Poppler for
compressed PDFs the built-in reader cannot parse, retaining size and time limits.

ZIP extraction checks entry count, declared sizes and actual copied bytes;
partial cleanup removes only the directory created by that attempt. ZIP sources
and output use separate authorized roots. `images_inspect` compares 1–8 images in
one model request, with a shared byte limit. `image_inspect` retains its single
path contract in ToolHub; direct MCP also accepts the old `paths` form. Every
array element is admitted. The reviewed catalog version is 1.2.0; existing pinned
selections must be reviewed/re-rendered for that version and its digest. The new
comparison tool needs an explicit profile selection; old grants are not widened
automatically. A call spanning different narrow directory grants
fails closed; split it into separately authorized calls.

## Routines (#247, #248, #250, #251)

One OS file lock fences the gateway lifetime against another gateway or an
offline writer. `apply` and offline routine commands require the gateway stopped.
Opening a spool for offline editing does not recover running jobs. Schedule
revision changes invalidate queued occurrences; deletion writes a permanent
tombstone and retired IDs cannot be reused. Already admitted Hermes work may
continue; queued delivery is checked again before sending. Cancellation cannot
recall a message already accepted by an external service.

Two background workers admit at most 16 oldest eligible occurrences per pass and
one per source. Due occurrences inside their catch-up window take priority over
expired backlog, so recording old misses cannot starve a fresh reminder. The
16-settlements-per-pass bound is unchanged. HTTP requests have a 10-second deadline and a 1 MiB response limit;
public-IP validation occurs at connection time, ambient proxies and redirects
are disabled. Private endpoints and credential-bearing URLs are refused.
The default probe compares a body or first regex capture. A missing capture is
an error, not an empty successful value. `github_issue` accepts the exact public
GitHub issue REST URL and compares only `state`; it needs no account and supports
no private repositories. Checks accept `changed`, `unchanged` or `equals:<value>`.
Creation validates ownership before its baseline request; schedule and initial
value are committed together.

Canonical probe/script/digest receipts precede outbox handoff. A retry replays the
receipt instead of repeating completed work. On Linux, file contents and parent
directory are synced. Windows syncs contents before rename; directory fsync is
unavailable through Go's os.File. This is not a cross-file filesystem transaction.
Failed writes leave replayable work; malformed completed records require repair.

Watchers can set `notify_mode=digest`; digest `source_ids` selects owned sources
(maximum 128). Default mode includes the caller context's sources. Digest cursors
track each source's generation, include at most 16 entries per message and retain
unreported changes. Errors include the last successful observation; the baseline
is preserved. The same unresolved error is listed once, with persistent
unavailability summarized on subsequent digests, so failed sources cannot
permanently consume every digest slot. `GET /v1/routines/<id>` returns schedule and watch state.
`POST /v1/routines/<id>/snooze {until: RFC3339}` postpones checks and invalidates
old queued occurrences. Deterministic digests do not call a model.

Reminder snooze replaces a one-shot deadline and rearms that same schedule with a
new revision, including when the old occurrence is already queued for delivery.
For recurring schedules, snooze postpones an earlier next due time to `until`;
subsequent runs retain the original cron expression and owner timezone. A weekly
Friday 10:00 Moscow reminder therefore stays on that weekly cadence after snooze.
The catch-up window is measured from the deferred deadline, saved on the occurrence
and honoured by both scheduling and dispatch (one hour by default). A late tick
outside it writes a durable `missed` occurrence before advancing or retiring the
schedule. Recovery admits at most one occurrence, never a burst of missed slots.
An expired pending reminder from an earlier slot does not block the current slot
of its recurring schedule; agent jobs keep their existing overlap fence.
Existing snoozes written before `NextDue` was updated also use `snoozed_until` as
their effective deadline; no bulk rewrite or replay of already-retired reminders
is performed.

Owner routine reads include derived `last_occurrence_state` and
`last_delivery_state` for reminders. The latter is `delivered` only with a completed
outbox record; a failed/unknown transport answer is `uncertain` and is not resent on
restart. `/routines` also names missed and unconfirmed deliveries. Occurrence detail
is read from the hot spool or its existing archive; after detail retention expires,
or if that detail cannot be verified, its state reads `unavailable`. These read-only
fields do not change creation idempotency or authorize access to another owner.
An unverified or expired occurrence never exposes the state of an outbox receipt
through a caller-supplied history reference.

Starlark routines require the supervisor. It starts a disposable core-image
container with no network, mounts or injected secrets; read-only filesystem,
unprivileged UID, dropped capabilities, 128 MiB memory/swap ceiling, 0.5 CPU and
16 PIDs. The helper has a five-second wall watchdog and a step budget. Source is
at most 16 KiB; emit allows four strings of 4 KiB. The supervisor bounds the
request to 15 seconds and removes only its named helper. An unsupervised gateway
fails a script explicitly rather than executing it inside the control process.

## Knowledge (#249)

Sources remain operator-staged snapshots and connector exports, served GET-only.
No guessed provider API or new live connector is introduced. Shared operator
control tokens address a user's personal context. Enrolled runtime tokens bind
the exact context; `X-Hub-Context` cannot switch it. Disabled users and revoked
tokens are rejected on every request. Reads reject unsafe names, non-regular
files and bodies exceeding 9 MiB. `knowledge_search` returns up to eight matching
lines with source, line number, SHA256 and modification time. It scans at most
128 sources and approximately 32 MiB (the last bounded source may exceed the
remaining budget). Content is untrusted evidence and cannot change tool policy.

## Usage and Telegram (#194, #195, #214)

The version-2 journal records deltas of cumulative session counters, keyed by
scope, context and session, once per job/run. The first snapshot is marked as
potentially including earlier usage; a reset or missing counter is unknown.
Legacy version-1 totals are excluded from measured token sums and separately
counted. No prices, durations or missing API counters are invented. An interrupted
final append is quarantined before truncation; corrupt completed lines stop writes.
`/usage [day|week|month] [model=<id>]` reads the journal and configured
provider subscription quota without waking Hermes.
`/usage session` explicitly queries the session and may wake its runtime.
Background metering only queries an existing runtime, before terminal handoff
releases the context. `/status`, `/routines` and `/connectors` are deterministic.
Inline approvals retain Telegram forum/direct-message topic routing. Explicit
session usage queries have a two-query limit and stop before the gateway releases
its spool lock.

Runtime idle time starts after the last activity lease is released. Active jobs,
streams and approvals keep their existing lease protection. Unit and mocked
contract checks do not establish live Telegram or production lifecycle acceptance.

## Mock and offline apply (phase C)

Mock chats bind atomically to one owner. Client IDs include that owner; identical
IDs across users create independent jobs. Attachments are captured as bytes in
the owner's mock receipt and never sent to Telegram or Slack. This tests the real
queue pipeline but does not prove an external channel integration.

`apply` rejects unknown fields, multiple YAML documents, duplicate resources,
unsafe knowledge names, excessive input and implicit delivery routing. It checks
the whole input before writes; an I/O failure during application may still leave
partial results, which the returned error identifies. It remains additive:
absence does not delete a resource, and offline `kind=check` is refused because
it requires the live baseline request. All routine mutations require explicit
`--chat-id`, `--organization`, `--runtime`, `--policy` and `--user` routing.
Go pins are 1.27.2 across host and image builds; BuildKit paths are unchanged.
The lint gate pins Staticcheck to upstream commit
[452d5bb86b45](https://github.com/dominikh/go-tools/commit/452d5bb86b45416c35891262f6a11dbd6a970117)
(pseudo-version v0.7.0-0.dev.0.20261009230814-452d5bb86b45): release v0.8.1
cannot decode this toolchain's export data. The compatible upstream tool is used
without disabling checks or changing application dependencies.

[Verification evidence](../.work/done/CHG-0083-m10-stabilization/state.yaml)
records the actual gates; no mock result should be described as live acceptance.


Local Docker acceptance uses dedicated verification tags and temporary contexts.
The managed supervisor canary namespaces its runtime and control bridges; fixture
ToolHub/model DNS aliases must never enter an existing deployment network.
Cold-start test budgets include the existing four-minute supervisor readiness
allowance plus execution; warm execution and authorization checks keep their
original bounds. Live Telegram acceptance remains a separate test.
