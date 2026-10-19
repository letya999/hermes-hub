---
description: Routines as a leaf package — semantics plus engine behind a Deps seam, sharing the spool lock and root.
last_verified: 2026-10-18
---
# ADR-0048: `internal/routines` — leaf package owning routine orchestration

Status: Accepted 2026-10-18. CHG-0118 isolated the tick; CHG-0125 moved the
semantics and then the engine. Planned as ADR-0046 in the phase plan,
renumbered (that number went to the zone map).

## Context

Routine orchestration — schedule ticking, occurrence admission, watcher
probes, digests, proactive follow-ups, script runs, reminders and
destinations — was ~4k lines of `Spool` methods inside the gateway package.
It pulled provider checks, task/mapping reads, delivery enqueue and runtime
script execution through the same object, so extraction threatened either an
import cycle or a second concurrency model.

## Decision

- `internal/routines` is a leaf: it imports `commodel` and `identity`,
  never `communication`, `channel`, `toolhub` or services.
- `routines.Engine` holds the spool's `*sync.Mutex` and root path — one
  concurrency model, no second writer. `OpenSpool` constructs the engine and
  keeps `Spool` forwarding methods so existing callers and tests are
  unchanged.
- Everything the engine needs from the outside arrives through
  `routines.Deps`: job/delivery enqueue, mapping/task reads, topic binding,
  delivery blob writes, delivery normalization, atomic JSON, notify
  signaling, schedule-draft hooks, event settlement — all closures.
- Channel access is the local `OccurrenceChannel` interface
  (`AuthorizeOccurrence`/`ValidateOccurrence`/`PrepareRoutine`/
  `ValidateSchedule`) injected via `Deps.ChannelFor`; `internal/channel` is
  never imported.
- Script execution is `Deps.ProbeScript` returning a `ProbeScriptFunc` — the
  gateway decides supervision, the engine only calls it.
- Durable formats and IDs are unchanged: the same `schedules/`,
  `occurrences/`, `watchers/`, `digests/` directories under the spool root,
  and goldens freeze occurrence hashes, routine conversation IDs and failure
  digest job IDs.

## Consequences

- `communication` shrinks to the gateway: HTTP/control surface, spool
  mechanics, channel dispatch, gateway-specific orchestration.
- An agent editing routine semantics cannot touch provider or gateway code;
  the boundary test enforces it.
- Routine data stays in the comm spool — physical separation would be a new
  layout decision requiring its own migration phase (see ADR-0050).
