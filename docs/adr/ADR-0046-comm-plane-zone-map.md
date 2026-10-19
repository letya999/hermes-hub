---
description: Communication-plane separation — zone map and package owners inside the existing processes.
last_verified: 2026-10-18
---
# ADR-0046: Communication plane zone map

Status: Accepted 2026-10-18. Implements the comm-plane separation milestone
(CHG-0118…CHG-0125); planned as ADR-0044 in the phase plan, renumbered
because that record went to routine destinations.

## Context

`internal/communication` fused the domain model, provider adapters, routine
orchestration, credential intake, knowledge serving and ops surfaces in one
package. Provider names sat in core conditionals; a new channel meant ~10
files and 5 switches the compiler never checked.

## Decision

Responsibilities move to owners; no new services — leaf packages inside the
same processes:

| Zone | Owner | Left `communication` |
|---|---|---|
| Durable domain model | `internal/commodel` — Job, Delivery, Task, User, Schedule, RoutineOccurrence, JobMapping, Event, FileID | All struct definitions; communication keeps `type X = commodel.X` aliases |
| Channel seam | `internal/channel` — `Channel`, `Caps`, contract errors, telegram/slack/mock adapters behind `Deps` structs | All provider code and switches |
| Routine semantics + engine | `internal/routines` — cron/timing/validate/extract/recipes/ids plus `Engine` (schedules, occurrences, watchers, digests, proactive, script runs) | ~4k lines of Spool routine methods |
| Ops plane | `internal/commops` — canary, health block behind `Deps` | canary/health loops |
| Service authorization | `internal/serviceauth` — bearer, `X-Hub-Principal`, tokens file | copies in http/knowledge handlers |
| Quota lookup | model-relay `/v1/quota` | CLIProxy management round-trip (CHG-0119) |
| Knowledge | runtime state `knowledge/<principal>` read by agenttools in-process | `/v1/knowledge` + spool tree (CHG-0121) |
| Credential intake | Broker; `/v1/self-env` for unmanaged mode | secrets.Service, audit ledger, ToolHub store attach (CHG-0120) |

Import direction is fixed by parser boundary tests: leaves never import
`communication`, `toolhub` or services; the gateway wires deps at
construction (`OpenSpool`, registry constructors).

## Consequences

- Core stays channel-agnostic: provider names appear only in the registry
  table and adapter files.
- Every phase is a separate release: durable paths and formats unchanged
  (see ADR-0050 for the single data relocation).
- Compatibility aliases in `communication` keep the exported contract and
  all call sites compiling while internals move.
