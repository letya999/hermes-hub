---
description: Routine business tools, interval windows, lifecycle, authorization and delivery evidence.
last_verified: "2026-11-20"
---
# Routines
ToolHub owns the routine clock, occurrences, dispatch and automation delivery intents.
Communication owns channel intake, delivery and receipts; reminders avoid model wakes and agents/probes use live rights. Research jobs persist separately and replay their Supervisor job ID after restart; research_read shows the final report only after completion.
`media_speak` uses this outbox for the active run's recorded destination: `delivery_id` and `status=queued` acknowledge enqueue, not sending. The worker rechecks `channels.send_voice`; synthesis/sending can still fail and uncertain sends are never blindly repeated.
In the mock channel, the worker synthesizes audio and stores its bytes in the owner-scoped mock outbox; only a captured `voice_sent=true` record proves mock delivery. The ToolHub admission lock is released and authority rechecked before the communication callback, which asks ToolHub for the live send right.
The M32 cutover places the clock, assignments, watches and protected nightly
dreaming in ToolHub (ADR-0095, SPEC-0094). Dreaming is a protected system
schedule with a normal routine occurrence and due recovery, keyed once per
owner and local date. Its operation processes memory and queues its digest
through the normal delivery outbox; ordinary routine CRUD cannot change it.
A night whose operation fails three times (retried after 1 and 2 minutes)
settles `failed`, so it never blocks later nights.
The schedule time and timezone come from the current published passport.
Memory disablement overrides tool selection. Calls require `tools.use` and
their own `tasks.schedule`, `tasks.observe` or `tasks.delegate` right.
Settings edits require current `config.edit_own` and `settings_read`'s revision; interrupted publications recover under the apply lock.
Edits may respawn the runtime, so same-turn restore is uncertain.
ToolHub reads gateway bindings from files or activated SQLite in place, read-only; cutover refuses legacy automation records/jobs until migration.
New occurrences persist the current supervisor admission epoch; holds block admission and retries never adopt a later epoch.
Decision: [ADR-0042](adr/ADR-0042-routine-lifecycle.md). Requirements:
[SPEC-0056](../specs/active/SPEC-0056-routine-lifecycle.md). Acceptance:
[CHG-0112](../.work/done/CHG-0112-routine-lifecycle/state.yaml).
The five-tool interface is amended by [ADR-0043](adr/ADR-0043-compact-routine-tools.md)
and [SPEC-0057](../specs/active/SPEC-0057-compact-routine-tools.md); current acceptance:
[CHG-0113](../.work/done/CHG-0113-compact-routine-tools/state.yaml).
Destination routing follows [ADR-0044](adr/ADR-0044-routine-destinations.md) and
[SPEC-0058](../specs/active/SPEC-0058-routine-destinations.md); rollout evidence:
[CHG-0114](../.work/done/CHG-0114-routine-destinations/state.yaml).
The post-acceptance snooze correction is tracked in
[CHG-0115](../.work/done/CHG-0115-routine-snooze-audit/state.yaml).
The opt-in owner brief follows [ADR-0041](adr/ADR-0041-proactive-automation.md) D5
and [SPEC-0054](../specs/active/SPEC-0054-proactive-automation.md) R4; rollout
evidence: [CHG-0102](../.work/done/CHG-0102-owner-brief/state.yaml).
Deterministic and MCP contract tests do not establish deployed acceptance.
## Business tools
| Tool | Contract |
|---|---|
| routine_create | Save ID, timezone, expression, text, action and destination; return configuration and deadline. Repeating the same intent returns current state. |
| routine_list | Find compact cards sorted by ID. `view=destinations` instead lists live owned session/task names and routing IDs. Query and cursor work in both views. |
| routine_read | Full configuration, revision, state, named saved destination and its availability, baseline/errors and event health. Optional include=[history,next_runs] adds bounded history and planned dates. A known retired ID remains readable. |
| routine_edit | Apply action=update, pause, resume or snooze using expected_revision. update takes patch, including destination_task_id for moving future results; snooze takes until RFC3339. pause/resume accept neither field. |
| routine_remove | Permanently retire ID, retain evidence and stop admission. Already sent messages remain sent. |

There are exactly five public routine tools. For edit, update requires a nonempty
patch. Pause stops future admission, including stream responses already in flight.
Resume advances recurrence from now; expired once schedules need a new due time.
Snooze defers one next occurrence and retains recurring cadence. Its `until`
must be later than the current `next_due`; an earlier time is rejected instead
of being reported as a successful move. Read `next_runs` again after editing.

Get defaults to the card alone. include=[history] uses history_limit 1..100,
default 10, and history_cursor from history.next_cursor. include=[next_runs] uses
preview_limit 1..20, default 5. Both can be requested together. History keeps its
items/next_cursor page; next_runs contains state/timezone/times. No work is run.
These component reads are not one atomic snapshot; any failed component fails
the call. Structured history answers user questions; diagnostics expose bounded lifecycle
metadata only. Search stable IDs/status fields; message bodies are excluded.
Repeated source_position marks replay; diagnostic_gap/truncated cannot prove success.
Toolhub `status=uncertain` / `retryable=false` identifies an unresolved operation;
retain operation_id and reconcile it before any repeat. It is an application
error, not permission to retry a transport. Only verified setup failure before
business dispatch permits one authentication recovery.
SSH uncertain results require reconciliation before repeating a command, write
or session action. Shell/tunnel close still attempts access termination if audit
fails; uncertainty is not a confirmed receipt.
Exposure errors or list/status uncertain=true require reconciliation of the existing
endpoint before repeating open. Close still attempts to stop access if audit fails;
an uncertain close is not a confirmed completion receipt.

Operations stay inside the authenticated principal and context. Identity and
audience are host-derived. List includes saved paused/completed schedules; use
get/history for a known retired ID. Paging is not a frozen snapshot: repeat
discovery if records were added during the scan.

Agent mutations require a revision observed in get/list. Check and write occur
under one spool lock. HTTP 409 means read again and deliberately reconsider the
edit. The adapter preserves status/reason and never automatically retries.
Internal legacy HTTP/operator callers retain their unversioned contract; the
agent catalog requires the fence. Edit preserves the server acknowledgements:
pause/snooze and delete return a durable card; create/update/resume return the
saved schedule. Unknown edit actions or contradictory patch/until arguments fail
before HTTP. A failed read after a write is never a reason to blindly retry.

Patch fields: input, expression, timezone, job_kind, probe, expect, max_retries,
notify_mode, quiet_from, quiet_to, daily_limit, source_ids, sink, delivery,
catch_up_window_ns and timing. Clear optional strings with `""`, arrays with
`[]`, and optional probe/timing with `null`. Required fields cannot be null.
Raw chat/topic routing, identity and server outcome fields are rejected. The
single allowed routing patch is `destination_task_id`.

Text edits preserve due, snooze and pause. Timing edits recalculate due, clear
snooze and reset the interval anchor while preserving pause. Kind conversion
retains ID/history, clears incompatible settings and validates the target action;
supply required configuration in the same patch. A check source edit takes a new
authorized baseline before commit, with a revision fence across that read. Source
revisions isolate watch baselines and event IDs; stale in-flight results cannot
settle into the new revision.

## Probe and event internals

- One OS file lock fences the gateway against other gateways/offline writers;
  `apply` and offline routines require it stopped; offline opening cannot recover jobs.
  Schedule revisions invalidate queued occurrences.
- Delete permanently retires the ID. An owned tombstone durably stops dispatch
  retries after restart, preserving the unknown outcome of already admitted work
  and accepting late results. Pause, unreadable state and foreign tombstones do
  not discard pending work. Delivery rechecks cancellation; accepted messages cannot be recalled.
- Tick admits ≤16 oldest eligible occurrences per pass, one per source; due
  occurrences inside catch-up take priority over expired backlog so backlog
  recording cannot starve fresh reminders. 16 settlements/pass bound
  unchanged.
- HTTP probes: 10 s deadline, 1 MiB response cap, public-IP validation at
  connect time, ambient proxies + redirects disabled; private endpoints and
  credential-bearing URLs refused. Fetch is separate from extraction: an
  ordered `extract` pipeline (`css`, `html2text`, `regex`, `grep`,
  `drop_regex`, `drop_lines`, `sort_lines`, `strip`, `json_path`, `rss`;
  ≤8 steps) reduces the body to the compared value; a failing/empty step is
  a probe error, never a silent "unchanged". Missing regex capture = error.
- `kind=script`: fragment reaches the disposable container via `input()`;
  exactly one emitted value required. `kind=command`: no fetch — runs with
  only the owner's `spaces/<user>/watch` read-only at `/watch` and bounded
  `read_file`/`list_dir`/`hash_path` builtins. `needs[]` (≤4 read-tool
  calls, ≤64 KiB each via `needs()`) prefetch under the owner binding; a
  failed need is a probe error, container stays networkless.
- `kind=tool` normalizes to `mcp_tool`: owner-granted read tools via `/v1/tool-call` (structured-only results become JSON text).
  `kind=render`/`kind=service` POST a bounded
  structured request (kind/url/args/steps/wait_for, ≤512 KiB response) to an
  operator-registered `service_endpoints` entry — never a schedule-supplied
  URL; render steps are a declarative browser-action list. `github_issue`
  takes an exact public GitHub issue REST URL, compares `state` only.
- `probe.recipe` expands builtin presets into snapshotted schedule fields at
  create; explicit fields win, unknown recipes rejected. Checks accept
  `changed`/`unchanged`/`equals:<value>`; creation validates ownership
  before the baseline request — schedule + baseline commit together.
- Starlark routines run only under the supervisor: disposable core-image
  container, no network/mounts/secrets, read-only fs, unprivileged UID,
  dropped caps, 128 MiB memory+swap, 0.5 CPU, 16 PIDs; source ≤16 KiB,
  ≤4 emit strings of 4 KiB, 5 s watchdog + step budget, supervisor-side
  15 s bound. Unsupervised gateway fails scripts explicitly.
- Receipts precede outbox handoff; a retry replays the receipt. Linux syncs
  contents + parent dir; Windows syncs contents only (no dir fsync via Go).
  Not a cross-file transaction — failed writes leave replayable work;
  malformed completed records need repair.
- Digest: `source_ids` ≤128 owned sources, default = caller context's
  sources; ≤16 entries/message, cursor tracks per-source generation,
  unreported changes retained. An error names its last success; the same
  unresolved error lists once (subsequent digests summarize persistent
  unavailability) so a failed source cannot permanently fill every slot.
  `GET /v1/routines/<id>` returns schedule+watch state;
  `POST /v1/routines/<id>/snooze {until}` postpones + invalidates queued
  occurrences. Deterministic digests never call a model.
- Snooze: reminder snooze rearms the same schedule at a new revision —
  including when the old occurrence is already queued; recurring snooze
  defers `next_due` to `until` keeping cron+timezone. Catch-up measures
  from the deferred deadline, saved per-occurrence; late tick writes
  `missed` and advances; recovery admits at most one occurrence.
- Derived reads: `last_occurrence_state`/`last_delivery_state` —
  `delivered` only with a completed outbox record; `uncertain` never
  auto-resent; `/routines` names missed/unconfirmed. Detail reads hot spool
  or archive; expired/unverifiable detail → `unavailable`. Read-only fields
  never expose outbox state through a caller-supplied history reference.
- Notification policy (`quiet_from`/`quiet_to` HH:MM owner-tz window;
  `daily_limit` 0..32 → `capped` with baseline advancing;
  `notify_mode=digest` feeds daily digest; `notify_mode=agent` admits one
  untrusted-flagged job — never on reminder/script/digest kinds) and
  `delivery=stream` internals are in architecture.md §Jobs, runs and
  events.

## Event subscriptions (watches)

A watch is an event-kind schedule subscribed to one `mcp_tool` source through
`watch_create`: its probe is `{kind:mcp_tool, tool, args}` and the sink is
`notify` (one message per fresh event) or `agent` (one job per batch, events
flagged untrusted). The probe never calls a model — unchanged sources and
`filter`-dropped events cost zero model wakes. `filter` is a Starlark script
evaluated per fresh event emitting `keep|drop|urgent`; an unevaluable filter
fails closed — events stay unseen and the round records an error.

`delivery=stream` runs one supervised loop per schedule holding one bounded
wait-call outstanding (a default bound is injected when the tool declares
none). Wait-style tools — names containing `wait`/`settled`/`new_message` —
are bounded long-polls and need no interval; a plain poll tool must declare
`probe.min_interval_seconds` (5..3600 s) as its source cadence contract,
between starts of successful rounds (read time counts). Validation rejects stream
on non-`mcp_tool` probes and on poll tools without a declared interval; it
also rejects probes on non-probe job kinds, pointing agent-kind misuse at
`watch_create`. The cron expression anchors liveness bookkeeping only —
it is not the poll cadence — and the global one-minute floor for ordinary
scheduled routines is unchanged. Before each stream round, the hub refreshes the
supervisor hold projection; a failed refresh blocks acquisition, and an admitted
job carries the current operational epoch. Routine conversations remain stable
per schedule and are hashed when a long schedule ID would exceed the runtime
identity limit.

Events dedupe durably per schedule revision; `next_cursor` persists for
restart resume; batch settlement fans out once — sink=agent wakes the model
once per batch, not per event, honoring quiet windows, daily limits and
assignment wake budgets. Connector auth failures mark the subscription
`degraded` instead of emitting fake empties. `watch_list`, `watch_edit`
(pause|resume) and `watch_remove` are scoped to event-kind schedules;
`routine_edit` update/pause/resume manages them under the same revision
fence. One subscription should cover the broadest scope the source tool
offers (e.g. account-wide args); narrowing belongs to `filter` — durable
dedup rejects repeats before they can spend a wake.

`probe.sources` fans one watch out over up to 32 inputs —
`[{name?, tool?, args}, …]` merged onto the base `tool`/`args` (source keys
win; `tool` may point at a sibling read tool; unnamed sources get stable
positional labels `s0`, `s1`, …). Sources are stream-only. Each source owns
an independent durable cursor, error and dedup namespace (`label|native-id`
so connectors reusing ids cannot collide); one round polls all sources
under a bounded concurrency of 8, merges survivors into a single filter
pass and a single wake, while a failed source records `source_errors`
without starving the rest — an all-source failure blocks the round.
Agent-sink bundles attribute every event as `[label] text`.

For non-consuming cursor reads, set `probe.cursor_arg` to the projected tool's
cursor parameter. The hub supplies the last durable `next_cursor` to each
source's next call. A never-initialized source may establish a silent
high-watermark baseline; loss of an initialized cursor fails closed with a
visible gap and requires recovery. Cursor state lives in `event-state/` outside
terminal-history compaction. Dropped events and successfully admitted events
advance it, while overflow, filtering failure and sink failure leave it in
place so the next poll replays the unhandled page. A source response without
`next_cursor` fails closed. For Telegram group messages use the opt-in
`watch_chat_messages` read tool (`cursor_arg=cursor`, `min_interval_seconds`
set, one `sources` entry per chat, explicit `account` in multi-account mode);
the old wait digest omits most group text.

`filter` receives the whole event, not only text: `input()` returns
compacted connector JSON whenever the source item is object-shaped (all
fields — `chat_id`, `sender_id`, `url`, …), falling back to plain text for
scalar items. The script must call `input()` as a function and end with
exactly one `emit("keep"|"drop"|"urgent")`. Structured filters can use
`event = json.decode(input())` and test fields such as `event["chat_id"]`
and `event["text"]` rather than matching JSON substrings.

`watch_sample` (`POST /v1/routines/{id}/sample`, owner-scoped) runs one
bounded probe round and returns up to four per-source raw event JSON items plus per-source
errors — never treating a dead connector as an empty result. An optional
`{"filter": "…"}` body dry-runs a candidate filter and reports the verdict
per sampled event. Sampling writes no seen marks, advances no cursor,
sends nothing and never wakes the model — it is the schema-discovery and
filter-dry-run step before committing a filter to a broad-scope watch.
Sampling a consuming `settled` wait is refused; a cursor probe samples a
historical page without altering its live baseline.

## Standing assignments

A standing assignment (`asg-*`, `internal/commodel.Assignment`) is the
durable owner instruction behind a set of routine artifacts (SPEC-0080).
It binds the verified owner envelope — never a Hermes session — and is
managed through `POST/GET /v1/assignments`, the `assignment_*` agent tools
and the `/assignments` `/assignment <id> …` chat commands.

- Save the owner's intent as a draft immediately, unless the owner explicitly requests `assignment_plan` first.
  Check public Telegram sources in batches of at most four with non-consuming
  `watch_chat_messages` bootstrap reads; persist checked accessible sources and
  `plan.unavailable` entries with reasons through CAS-guarded plan updates.
  Invite, discussion and website links require separate access checks and
  cannot be declared active from their URLs. A late model failure leaves the
  draft and the last saved source-check batch available for resumption.
- Public `t.me/<username>` and `t.me/s/<username>` links in a Telegram cursor
  plan are normalized to `chat_id=<username>`; private/invite links fail
  validation before apply. One event source fans out over all verified chats
  at the owner-requested `min_interval_seconds` and merges candidates into one specialist job.
- Create stores intent verbatim plus a validated plan (`sources` +
  optional `specialist`). Replayed creates require the same owner, intent,
  title, plan and grants; direct creation cannot exceed default grants.
  An assignment event source with `sink=agent` must declare a deterministic
  `filter`, so raw source traffic cannot wake the specialist indiscriminately.
- `apply` reconciles the plan into deterministic schedule ids
  (`asg-<assignment>-<source>`), stamping the owner envelope, actor,
  scope and organization onto every artifact; receipts record
  `created|kept|replaced|removed` per source so re-apply is idempotent
  and a retired id is recreated with a `-rN` generation suffix, never
  resurrected. For a one-time first HTTP read, use `watcher` with `deliver_mode=content`; `check expect=unchanged` requires a prior baseline. A completed `once:` schedule may finish its in-flight probe after retirement.
- Lifecycle propagates to artifacts: pause/resume flip the applied
  schedules, cancel/complete tombstone them and fence the record
  terminal; apply and plan updates refuse a paused assignment, which leaves
  pause only through resume; wakes are fenced by the applied plan generation at admission
  and claim, so a paused or re-planned assignment cannot run a stale job. The completing trigger's confirmed response remains sendable after its own source retires and occurrence moves to archive, while deleting the assignment or revoking delivery rights still blocks it.
  An active owner assignment may use the owner's currently selected and
  granted memory tools under the same `memory.enabled` switch. The exact
  owner, job, run and generation are checked at call time and before writes;
  assignment output is not automatically a trusted owner memory fact.
  ToolHub carries assignment and untrusted provenance to supervisor and uses
  its run stream so the exact run ID is durable before model tool calls.
- For judgment-only candidate batches the specialist records progress and
  returns exactly `NO_ACTIONABLE_EVENT` when none merit a notice; the gateway
  suppresses that sentinel and routine status chatter. A useful finding is
  delivered once through the usual bound Telegram job reply with the requested
  mention. Failure notices remain visible.
- The specialist receives the owner intent, previous summary and next step
  with each filtered trigger. `assignment_edit action=progress` requires that running
  trigger's job id and current assignment revision, and stores a bounded
  summary, one new finding and next step; `complete=true` retires sources
  only when the owner-defined completion condition is met.
- Mutations carry `expected_revision` CAS. Grants (`max_wakes_per_day`,
  `max_sources`) only narrow through APIs — widening requires the
  chat-only `/assignment <id> authorize <max_wakes_per_day> <max_sources>`
  verb, which accepts explicit limits for the next plan generation.
- Every route and command is owner-scoped: foreign principals get the
  same 404/refusal as a missing record.

## Source acquisition

HTTP-polling schedules share a per-sweep acquisition pool keyed by
`(principal, url)` (SPEC-0081): same-owner subscribers of one URL cause
one fetch per probe sweep, never across principals, never stale across
sweeps. A source `Retry-After` (HTTP 429/503, plus GitHub-style 403 with
`Retry-After` or an exhausted `X-RateLimit-Remaining`/`X-RateLimit-Reset`
budget) is persisted durably and fences later polls until the deadline;
durable subscriber state tracks `fetched_at`, `next_poll_at`, `failures`
and `gap_count`.

`kind=hook` schedules carry a `hook:<label>` url and take no fetch: an
authenticated owner-scoped `POST /v1/hooks/<schedule-id>` writes the body
into the schedule's durable inbox (`data/hooks/<schedule>/`), and each
probing occurrence drains the oldest staged payload into event settlement,
preserving source IDs. Empty envelopes/inboxes are idle ticks (`no_change`).
Intake is restart-safe because staged payloads live on disk.

## Schedules and windows

| Expression | Meaning |
|---|---|
| after:180s | Creation-only relative reminder, resolved once by the hub. |
| once:2026-10-19T10:00:00+03:00 | One absolute future instant. |
| 0 10 * * 5 | Five-field cron, Friday 10:00 in the supplied IANA timezone. |
| every:15m | Anchored interval, integral seconds from one minute through 8760 hours. |

Intervals retain their persisted anchor across restart. Optional timing supports
up to eight local windows, weekdays (Sunday=0), up to 24 extra local times and
absolute bounds:

```json
{
  "id": "weekday-stretch",
  "kind": "reminder",
  "text": "Встать и размяться",
  "timezone": "Europe/Moscow",
  "expression": "every:15m",
  "timing": {
    "windows": [{"from": "14:00", "to": "16:00"}],
    "weekdays": [1, 2, 3, 4, 5],
    "at": ["17:00"]
  }
}
```

This gives 14:00, 14:15, ..., 15:45 and 17:00 on weekdays. Opening is included,
close excluded; cadence begins at each opening. An overnight window belongs to
its opening weekday. Extra times form a union with interval slots; coincident
instants occur once. Without windows, extras supplement the unrestricted
interval. start_at is inclusive; end_at is exclusive. Exhaustion completes the
schedule. Gaps skip nonexistent local times; folds can yield two UTC instants.

Preview complex schedules before confirming them. state=paused means its dates
are hypothetical until resume; completed/deleted have no future slots. Windows
govern admission, not an admitted job's duration. Stream delivery uses its bounded
wait loop and rejects interval/window options.

Default catch-up is one hour. Downtime coalesces missed slots instead of replaying
a burst; expired work records missed and recurrence advances to a future slot.
Native Hermes cron must not own the same routine simultaneously.

## Delivery destination

The routine's Hermes work session is isolated from interactive chat. Its result
is delivered to the saved Telegram task and topic. Creation from a conversation
passes `origin_job_id` supplied in that run's instructions; this pins the exact
source task even if another conversation changes `/use` at the same time. To
choose a different session, call `routine_list` with `view=destinations`, search
by name, and pass the returned `task_id` as `destination_task_id`. The explicit
`default` target is the root chat. The public create tool requires an origin or
explicit destination; old private HTTP clients retain their prior behavior. Mock-origin reminders return to the claimed mock chat and its captured outbox.

`routine_read` displays the saved task name, chat/topic IDs, and `active`,
`archived`, or `unavailable` status. An archived task is marked `archived`;
a revoked or broken binding is `unavailable`. Neither is silently rerouted.
To move a routine, use
`routine_edit action=update` with
`patch={"destination_task_id":"<task_id>"}` and its current
`expected_revision`; for the current run's session use `current` and pass its
`origin_job_id`. The hub resolves and verifies ownership and topic binding.
Moving retains schedule ID, timing, pause, snooze and history. The next admission
uses the new audience; work already admitted keeps its original audience.

## Owner brief

A `brief` schedule composes one authorized snapshot per slot: changed watch
sources since the last slot (the shared digest cursor; `source_ids` narrows —
empty selects every owned watch), unavailable sources named unavailable with
their last success time, and the owner's open tasks. Every source re-checks
its grant at assembly; the admitted job and the deterministic delivery are
re-authorized again at claim and send. Snapshot lines carry the source URL
and change time as provenance and are bounded like watch values.

An empty snapshot sends one deterministic "Новых изменений нет." — the model
never runs. A non-empty snapshot admits a single agent job in the routine
conversation with the snapshot flagged untrusted; instruction text is where
length or section intent goes. The same failure episode is named once; later
slots carry only the unavailable count in the deterministic footer. A quiet
window defers the whole slot — a model call cannot ride a delivery
not_before — so still-due slots fire after the window and a window longer
than catch-up settles `missed`, never silent.

## Outcomes and retention

A completed once schedule has no future slots; admitted work remains retryable
under its revision, while edits, deletion and revocation fence it. `/routines`
separates schedule, execution and delivery state. Starlark probes use two slots
independent of warm runtimes; read deadlines include legacy snooze state.

A schedule the tick cannot safely advance becomes `blocked`: unreadable
schedule state and persistent owner-authorization failure stop the retry loop
instead of spinning every interval. The card shows `blocked_reason`, the owner
receives exactly one notice per cause at their verified home address, and a
successful update or resume clears the block; delete retires the ID. A single
denied occurrence record never blocks the schedule — stale records cannot
poison a healthy routine.

History state describes the occurrence; execution_state comes from a matching
agent mapping, otherwise mismatched evidence is unavailable. delivery_state
distinguishes queued, partial, delivered and uncertain; delivery_count includes
only the bound audience. Uncertain sends may have arrived and are not replayed
automatically. Delivered does not mean read.

ToolHub archives settled occurrences after delivery leaves both outboxes and
agent execution is terminal; live sends retain their authorization record.
Detail remains at least 48 hours after its last write. Owner-bound summaries
remain normally 72 hours after compaction. Legacy receipts without ownership or
outcome evidence are untrusted; streams have no clock occurrences (see event_state).

## Deployment and manual acceptance

The current agent-tool catalog is hub-agent-tools@18.7.1 (platform Release 42); old definitions remain immutable. For managed spaces, publish selected tools/grants, then rebind and respawn the runtime. The platform's new-space template selects `web_search`, `sessions`, `memory` and `media` through ToolHub; rights and service/provider configuration remain separate. `media_transcribe` resolves `/workspace/` paths into the same admitted workspace as relative paths, without accepting traversal. The browser contract gives each session its own tab; `browser_act wait` accepts text or numeric seconds up to 30, and close closes its tab. For a local skill import, `file_write` stages `SKILL.md` first; `skill_edit action=install_local` reads the same `/workspace`-relative path and returns `diff`, `lint`, `scan` and a confirmation card. The skill becomes active only after owner approval and managed runtime refresh. For requested post-delivery cleanup, keep a pending reminder active and remove it only after history confirms delivery. Assignment plans carry nonempty sources[].schedule and structural dry-run verdicts; apply follows a saved verified plan, while progress requires a real assignment trigger job. Mock assignment sources bind to the claimed destination chat envelope before their first tick. Settings publication may respawn the current runtime; a request requiring a same-turn restore stops after `settings_read`. `settings_read.manageable_keys` bounds the whole reviewed setting surface — an absent key cannot be changed through tools, and chat language lives in the `/language` channel command, not ToolHub settings. A call denied on a declared selection reports a denial distinct from the not-found an undeclared name returns; deny events audit a bounded reason class.
For recurring reminders, use `routine_create` with `kind=reminder` and an `every:` expression, then read the saved routine and its next due time. For a new watch, read its source through the projected ToolHub tool before creating it; `watch_sample` samples an existing watch. Stream delivery on a plain poll source requires `min_interval_seconds`. A blocked schedule records the safe authorization reason code for owner rights or channel binding; restore the actual right or binding before resuming.
`memory_open` belongs to memory; it grants no routine a viewing link. Routine calls require the current `tasks.schedule` right as well as independent
tool admission; selection does not grant the right. Current MCP discovery shows
the five routine tools plus the watch and assignment families. A catalog
upgrade creates no provider connection or external write grant.
For ToolHub onboarding, `you_must=click_link` means a protected link requires the user's verified Telegram callback in the original chat; other channels cannot complete this step yet. The callback binds actor,
action digest, revision and expiry and is consumed once; protected pages show
status only. The agent-facing `confirm` tool reads status only; a tool call
with a nonce never supplies human consent. Credential submission, OAuth
provider consent and an explicit credential-free install request each count
as the owner's consent — the install completes inline; separate confirmation
remains only for `allow_writes`, `remove` and drift gates. After Broker form submission, query `connector_status` by exact onboarding/operation ID. It reads saved state, you_must and the last observation; background Broker events advance the onboarding. An expired one-time form does not imply a lost credential, and transient admissions retry without discarding it. Waiting for credentials, OAuth or confirmation sends a deterministic notice without launching a redundant model continuation. If the link or button is missing, use `connector_edit action=resend` for that ID; it queues a notice. `connector_status` reads the exact notice's durable channel outcome in `delivery`+`channel`: `queued` is accepted pending, `sent` has the named channel's confirmation — never user read or click — `uncertain` is unconfirmed and `not_sent` means no notice entered the queue. Recheck status before claiming delivery; do not use reauth to recover a missing notice. A running install/edit operation also reports its journaled position — `current_step`, `step_index`/`step_total`, `elapsed_seconds` and `step_started_at` — so progress checks answer with the named step, not a guess. For an enabled browser connector, resend sends its viewer link. `reauth` explicitly opens credential entry even before setup confirmation and after a completed reauth cycle. `resend` repeats the current pending action; it does not replace submitted credentials. `disable` retains credentials; `revoke` cuts authorization, including a pending rotation target, without deleting the record. A missing old Broker credential cannot prevent an explicitly requested separate owner-scoped intake; its unknown status stays unknown. Never request secrets in chat. The effective system contract must be valid UTF-8; failed materialization preserves the previous readable file.

Use the [operations rebuild procedure](operations.md). Runtime dev overlays
update both hub-runtime and its hubctl executor. Verify actual managed MCP
arguments/calls, then verify external delivery separately. A legacy space can use
the same business tools through its existing binding; a catalog upgrade does not
migrate that space to managed access policy.

Three owner prompts for a manual check:

1. «Через две минуты напомни: проверить тестовое напоминание. Покажи ID и время».
2. «В будни каждые 15 минут с 14:00 до 16:00 и отдельно в 17:00 по Москве
   напоминай размяться. Сначала покажи ближайшие срабатывания».
3. «Каждую пятницу в 10:00 по Москве напоминай подвести итоги недели».

For routing acceptance, move a fixture reminder between two named topics; verify the destination and remove it.

## Source batch recovery

Event sources persist one bounded pending batch with exact filter decisions and
prepared sink payloads in their existing cursor state before fanout. A restart or
full sink drains that remainder before another source fetch; recovered events do
not rerun the filter. Drop decisions and sink admission precede cursor advance.
Pause, owner authorization and assignment/revision fences still apply to replay.
A schedule edit that moves the revision under a pending stream batch drops its
unadmitted events as a recorded gap (`gap_count`); it never blocks the owner's other streams.
Agent batch references additionally bind schedule and probe revisions at claim
and delivery. Sources with consuming reads and no ACK cannot guarantee delivery
across a crash before the first local receipt; prefer non-consuming cursor reads.
Failed agent turns keep their logical job identity and do not create automatic
`-r1` submissions. Failed goals pause; missing tool telemetry is not proof of no
effect. Same-identity retry is limited to verified pre-dispatch failures; unknown
results reconcile. CHG-0222 native/container acceptance remains pending.

Assignment wake checkpoints retain bounded unresolved `wake_operations`, keyed by
logical job ID with its applied generation. Replaying that reservation preserves
the original wake charge across enqueue failure or day rollover. Current pause
and generation checks still apply. A confirmed durable job admission releases
its reservation; failed confirmation leaves replayable local work. Event wakes
also check the durable mapping before reserving, so lost acknowledgements do not recharge.
Non-event watcher escalation saves exact job intent in its existing `probe-result`
receipt before enqueue. Replay keeps source/occurrence identity and current owner
checks; it does not refetch or charge the same reserved wake twice. Storage
failure is returned separately from an exhausted quota. A receipt from another
source or with another audience is refused before admission. Probe receipts are
regular files bounded to 4 MiB and decode as one known JSON document.
