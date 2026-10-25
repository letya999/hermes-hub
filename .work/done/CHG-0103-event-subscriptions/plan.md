# CHG-0103: Event subscriptions (M12 stage 5)

Chain: `docs/adr/ADR-0041-proactive-automation.md` D6 -> `specs/active/SPEC-0054-proactive-automation.md` R5
-> this plan -> `state.yaml`. Evidence: `../CHG-0099-followup-hardening/research.md`.
Previous stage: `CHG-0102`. GitHub issue: #257.

## Goal

`job_kind=event` schedules are durable owner subscriptions: each poll
extracts discrete events from the probe (connector `mcp_tool` results, or a
changed http/github value), dedupes by a bounded seen-set, and fans out to
`notify` (outbox delivery per event) or `agent` (one job bundling the new
events). `watch_*` agent tools and `/watches` expose the surface.

## Read before coding

1. SPEC-0054 R5, ADR-0041 D6.
2. `internal/communication/watchers.go` `settleProbe`/`probeOne`;
   `internal/communication/occurrences.go` dispatch branches;
   `internal/agenttools/routines.go`; `task_commands.go` command patterns;
   `internal/toolhub/prepared/catalog.json` telegram entry.

## Slices

### S5.1 Event state + extraction (R5.2)

Anchors: `watchers.go` (`WatchState` sibling), `probeValue`.

Change:
1. `EventState` in `spool/events/<id>.json`: `SeenIDs []string` (cap 512,
   FIFO) + `SeenSet map` persisted as list, `LastSuccess`, `LastError`,
   `Degraded bool`, `Overflow int`.
2. `extractEvents(probe, body) []Event{id, text}`:
   - `mcp_tool`: parse JSON — `{events:[…]}` else top-level array else
     scalar; item id = `id`/`event_id`/`message_id` field else sha256(item).
   - http/github_issue: a changed value is one event, id = new hash,
     text = bounded new value.
3. Dir `spool/events` joins the spool dir list.

Tests:
- `TestExtractEventsArray`, `TestExtractEventsFallbackID` (stable hash),
- `TestExtractEventsHTTPChange` (one event per change).

### S5.2 Event occurrence settle + sinks (R5.3, R5.4)

Anchors: `occurrences.go` probing branch (watcher/check -> `probing` —
  include `event`); `watchers.go` `settleProbe`.

Change:
1. `Schedule` gains `Sink string` ∈ {`notify`,`agent`} (event kind only,
   default `notify`); `validateScheduleDraft` enforces.
2. `DispatchDueOccurrences` routes `event` to `probing` like watcher.
3. `settleProbe` event branch: extract events -> drop seen ids -> sink:
   - notify: one Delivery per event, id `event-<jobid>-<eventid>`,
     `NotBefore` via quietDefer (stage 2), daily cap counts events;
   - agent: one `enqueueLocked` job, text = input + bundled event texts
     flagged untrusted;
   - mark seen ids + `last_success`; sink admission failure per event ->
     count `Overflow`, call `probe.requeue_tool` best-effort (stage 3
     transport), leave id unseen... document: seen written WITH the
     settled outcome so a pre-write crash replays safely.
4. Connector auth errors (`is_error` 401/403-shaped) -> `Degraded=true`
   in state + occurrence `failed`.

Tests:
- `TestEventNotifyFanout`: 2 new events -> 2 deliveries, deduped ids.
- `TestEventReplayDedup`: same poll result twice -> second run 0 deliveries.
- `TestEventAgentSinkBundles`: agent sink -> one job, events bundled.
- `TestEventOverflowCounted`: 10 events -> 8 deliveries + overflow 2.
- `TestEventDegradedOnAuthError`.

### S5.3 Agent surface + /watches + telegram catalog (R5.1, R5.5)

Anchors: `internal/agenttools/routines.go` `routineDescriptions`,
`Tools.Routine` switch, `Input` fields; `tasks.go`/command dispatch —
`connectionsCommand` pattern; `catalog.json` telegram `read_tools`.

Change:
1. `Input` gains `Sink`, `Tool`, `Args` (JSON string) — reuse Kind/URL/Regex.
2. `watch_subscribe` -> `routine_create` path with `kind=event` (+sink,
   probe tool/args); `watch_list/pause/delete` -> existing GET/POST/DELETE
   filtered to event kind in the response. Descriptions mirror routine_*.
3. `/watches` command in the gateway (like `/connections`): lists the
   caller's event subscriptions — id, source summary, sink, last
   success/error, degraded flag.
4. catalog.json telegram `read_tools` += `wait_for_new_message`,
   `wait_for_settled_message`, `requeue_event`, `incoming_feed_status`;
   `docs/prepared-connectors.md` telegram section notes the event tools
   and the persistent-workload dependency (stateful stays false — see
   limitations).

Tests:
- `TestWatchSubscribeCreatesEventSchedule` (HTTP-level, mock channel);
- `TestWatchesCommandLists` — B's subscription invisible to A.
- catalog test: telegram read_tools contains the four names.

## Red-team checklist

- `sink=agent` bundles flagged untrusted; event text capped per item and
  per bundle.
- Seen-set persisted before the next poll — replay across restart cannot
  double-deliver (delivery ids also dedupe).
- `requeue_tool` receives only the event id — never owner content.
- A subscription on a foreign principal's connector cannot exist: probe
  auth resolves the schedule owner's credential only.

## Acceptance

SPEC-0054 A7. Live: where a connector exposes an event tool, subscribe
`sink=notify`, produce one event, observe exactly one delivery; crash the
hub mid-fanout, restart, observe no duplicate.

## Out of scope

- Public webhook ingress; generic workflow engine; group-scope passive
  policy (#207); the persistent connector workload lifecycle (M13
  connector platform — documented limitation: event-capable sources poll
  at their schedule cadence until workloads persist).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team + live acceptance in state.yaml.
- [ ] docs/architecture.md + prepared-connectors.md updated.
- [ ] Issue #257 commented.
