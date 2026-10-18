---
description: Routine business tools, interval windows, lifecycle, authorization and delivery evidence.
last_verified: 2026-10-18
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

The catalog is hub-agent-tools@4.0.0. Old definitions remain immutable. Register
the new version and, for managed spaces, deliberately repin selected tools in the protected profile,
preserving scopes, denies and limits. routine_edit requires routines.update and
the update:routines tuple for all four actions. Do not translate a former
pause-only or snooze-only selection into this broader edit grant automatically.
Delete remains a separate capability. Old private operations still serve already
pinned definitions; current MCP discovery shows only the five tools. No provider connection or
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
