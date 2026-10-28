---
description: Routine business tools, interval windows, lifecycle, authorization and delivery evidence.
last_verified: 2026-10-28
---
# Routines

The communication hub owns the durable clock, occurrence queue and outbox.
Hermes interprets requests and calls business tools through ToolHub. A reminder
delivers its exact text without waking a model. Agent tasks use current runtime
authorization; deterministic probes/scripts use existing bounded paths. The hub
must be running to fire a routine.

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
| routine_get | Full configuration, revision, state, named saved destination and its availability, baseline/errors and event health. Optional include=[history,next_runs] adds bounded history and planned dates. A known retired ID remains readable. |
| routine_edit | Apply action=update, pause, resume or snooze using expected_revision. update takes patch, including destination_task_id for moving future results; snooze takes until RFC3339. pause/resume accept neither field. |
| routine_delete | Permanently retire ID, retain evidence and stop admission. Already sent messages remain sent. |

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
the call. Structured history answers user questions; raw logs remain diagnostics.

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

- One OS file lock fences the gateway lifetime against a second gateway or
  offline writer; `apply` and offline routine commands require the gateway
  stopped. Opening a spool offline does not recover running jobs. Schedule
  revision changes invalidate queued occurrences; delete writes a permanent
  tombstone and retired IDs are never reused. Admitted Hermes work may
  continue; queued delivery is re-checked before sending — cancellation
  cannot recall a message an external service accepted.
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
- `kind=tool` normalizes to `mcp_tool` — read-effect tools only under owner
  grants via `/v1/tool-call`. `kind=render`/`kind=service` POST a bounded
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
`watch_subscribe`: its probe is `{kind:mcp_tool, tool, args}` and the sink is
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
which the loop sleeps between successful rounds. Validation rejects stream
on non-`mcp_tool` probes and on poll tools without a declared interval; it
also rejects probes on non-probe job kinds, pointing agent-kind misuse at
`watch_subscribe`. The cron expression anchors liveness bookkeeping only —
it is not the poll cadence — and the global one-minute floor for ordinary
scheduled routines is unchanged.

Events dedupe durably per schedule revision; `next_cursor` persists for
restart resume; batch settlement fans out once — sink=agent wakes the model
once per batch, not per event, honoring quiet windows, daily limits and
assignment wake budgets. Connector auth failures mark the subscription
`degraded` instead of emitting fake empties. `watch_list`, `watch_pause`,
`watch_resume` and `watch_delete` are scoped to event-kind schedules;
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

`filter` receives the whole event, not only text: `input()` returns
compacted connector JSON whenever the source item is object-shaped (all
fields — `chat_id`, `sender_id`, `url`, …), falling back to plain text for
scalar items. The script must call `input()` as a function and end with
exactly one `emit("keep"|"drop"|"urgent")`.

`watch_sample` (`POST /v1/routines/{id}/sample`, owner-scoped) runs one
bounded probe round and returns per-source raw event JSON plus per-source
errors — never treating a dead connector as an empty result. An optional
`{"filter": "…"}` body dry-runs a candidate filter and reports the verdict
per sampled event. Sampling writes no seen marks, advances no cursor,
sends nothing and never wakes the model — it is the schema-discovery and
filter-dry-run step before committing a filter to a broad-scope watch.

## Standing assignments

A standing assignment (`asg-*`, `internal/commodel.Assignment`) is the
durable owner instruction behind a set of routine artifacts (SPEC-0080).
It binds the verified owner envelope — never a Hermes session — and is
managed through `POST/GET /v1/assignments`, the `assignment_*` agent tools
and the `/assignments` `/assignment <id> …` chat commands.

- Create stores intent verbatim plus a validated plan (`sources` +
  optional `specialist`). Replayed creates dedupe on the intent hash.
- `apply` reconciles the plan into deterministic schedule ids
  (`asg-<assignment>-<source>`), stamping the owner envelope, actor,
  scope and organization onto every artifact; receipts record
  `created|kept|replaced|removed` per source so re-apply is idempotent
  and a retired id is recreated with a `-rN` generation suffix, never
  resurrected.
- Lifecycle propagates to artifacts: pause/resume flip the applied
  schedules, cancel/complete tombstone them and fence the record
  terminal; wakes are admitted per assignment revision, so a paused or
  re-planned assignment never wakes a stale specialist job.
- Mutations carry `expected_revision` CAS. Grants (`max_wakes_per_day`,
  `max_sources`) only narrow through APIs — widening requires the
  chat-only `/assignment <id> authorize` verb, which stamps the plan
  revision as authorized.
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
probing occurrence drains the oldest staged payload into normal event
settlement. An empty inbox is an idle tick (`no_change`), not a probe
failure. Intake is restart-safe because staged payloads live on disk.

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
explicit destination; old private HTTP clients retain their prior behavior.

`routine_get` displays the saved task name, chat/topic IDs, and `active`,
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

Schedule state, execution and delivery are separate. A completed once schedule
has no future slots while its admitted work may still be pending. `/routines`
distinguishes completion from pause and omits zero-year deadlines. Read deadlines
include legacy snooze state.

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

Settled detail remains for at least 48 hours after its last write. Owner-bound
summaries follow spool retention, normally 14 days. Legacy receipts lacking
ownership/outcome evidence cannot become trustworthy history. Streams have no
clock occurrences; inspect event_state for current health/error.

## Deployment and manual acceptance

The catalog is hub-agent-tools@5.6.0. Old definitions remain immutable. Register
the new version and, for managed spaces, deliberately repin selected tools in the protected profile,
preserving scopes, denies and limits. routine_edit requires routines.update and
the update:routines tuple for all four actions. Do not translate a former
pause-only or snooze-only selection into this broader edit grant automatically.
Delete remains a separate capability. Old private operations still serve already
pinned definitions; current MCP discovery shows the five routine tools plus the
watch and assignment families. No provider connection or
external write grant is created by a catalog upgrade.

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

To check routing manually, create two named tasks/topics. In the first ask:
«Поставь тестовое напоминание через две минуты именно в эту сессию; покажи
сохранённый топик». Then ask: «Покажи доступные сессии для рутин и перенеси
напоминание в `<имя второй сессии>`; покажи новую карточку и прежнюю историю».
The next message should arrive in the second topic. Test this with a harmless
fixture and remove it afterwards.

Then ask to show all pages, open the card, pause, change text, resume, snooze,
show history and delete the fixture. Verify ID, revision, state and dates after
each change. Record real sends and cleanup as their own acceptance evidence.
