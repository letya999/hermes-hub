# CHG-0189: Watch tool contract discipline + event-watch discoverability (M23 stage follow-up)

Chain: `docs/adr/ADR-0082-standing-assignments.md` D5–D7 ->
`specs/active/SPEC-0081-shared-acquisition.md` R1.6/R4.1 -> this plan ->
`state.yaml`.
Previous stage: `CHG-0188`. Issues: #324, #332.

## Goal

The live ask «следи за группами и пришли интересные гитхабы» produced a
`routine_create kind=agent every:15m` that re-wakes the model on a timer —
the wrong tool. The watch machinery (stream wait-loops, durable dedup,
fail-closed filters, batch wake) already exists; the failure was contract
discoverability plus small contract gaps: `watch_subscribe` had no
`delivery`/`filter`/`min_interval_seconds` args in the catalog, the prompt
contract never mentioned watches, nothing rejected probe-shaped args on a
cron agent routine, and poll-style stream tools had no declared cadence so
the loop hot-spun. This stage closes those gaps and codifies the
tool-contract changeset rule (P0).

## Read before coding

1. `internal/stack/prompt_contract.go`, `internal/agenttools/{capability,tools,routines,call,tools_api}.go`
2. `internal/routines/{validate,timing}.go`, `internal/communication/{streams,events}.go`
3. `docs/tools.md`, `docs/routines.md`, `docs/engineering.md`, `AGENTS.md`
4. SPEC-0081 (acquisition/stream contract), SPEC-0080 (assignments)

## Preconditions and rules

- The global routine floor of 60s (`internal/routines/timing.go`) is not
  touched; sub-minute cadence is per-source via `probe.min_interval_seconds`
  on `delivery=stream` only.
- No provider-specific hardcoding — wait capability is a name-contract
  property (`commodel.WaitCapableProbeTool`).
- Tool-contract changes ship in one changeset: description, prompt contract,
  docs/tools.md + docs/routines.md, catalog version bump, live-space
  rebind + respawn. A cataloged tool missing from docs fails the suite.
- Live validation runs on `artem_main` only; `artem_two` is a different
  user and is not the acceptance environment.

## Slices

### S1 Contract discipline (P0)

Anchors: `internal/agenttools/tools_docs_test.go`, `docs/tools.md`,
`AGENTS.md`, `docs/engineering.md`.

- Every `HubToolsDefinition` tool must appear in `docs/tools.md` (wildcard
  `prefix_*` rows cover families); suite fails otherwise.
- Changeset rule recorded in AGENTS.md + engineering.md.

### S2 Discoverability and semantic routing (P1)

Anchors: `internal/stack/prompt_contract.go`, `internal/agenttools/routines.go`,
`internal/routines/validate.go`.

- Prompt contract gains the monitoring paragraph: monitor/follow asks ->
  `watch_subscribe`; kind=agent cron polling a source is named the misuse;
  sink/delivery=stream/min_interval_seconds/filter/watch management/
  assignment_plan escalation are all named.
- `routine_create` description carries the watch_subscribe pointer;
  `watch_subscribe` description documents broadest-scope + filter,
  wait-style vs poll-style stream, watch_resume/routine_edit management.
- `routine_create` rejects probe-shaped args (tool/args) on non-probe kinds
  with a watch_subscribe pointer; hub draft validation rejects probes on
  non-probe job kinds the same way (agent/reminder/digest/script/"").

### S3 Per-source cadence contract (P2)

Anchors: `internal/commodel/routine.go`, `internal/routines/validate.go`,
`internal/communication/streams.go`.

- `WatchProbe.min_interval_seconds` (5..3600) is the source cadence
  contract; required on stream delivery for poll-style tools, rejected on
  non-stream delivery; global 60 s routine floor untouched.
- `commodel.WaitCapableProbeTool` is the single name-contract predicate.
- `streamPoll` sleeps the declared interval between successful rounds;
  `next_cursor` persists into `EventState.Cursor` on stream batches.

### S4 Surface (P3)

- `watch_resume` tool (catalog, dispatch, direct MCP, docs).
- Catalog `hub-agent-tools@5.3.0`: `watch_subscribe` gains
  `delivery`/`filter`/`min_interval_seconds`.

## Red-team checklist

- Agent-cron polling a source is refused at the tool layer AND the hub
  draft layer with a `watch_subscribe` pointer — sibling callers cannot
  bypass via REST.
- Poll-style stream without a declared interval is rejected at draft
  validation; wait-style tools keep working without one.
- Stream batches persist `next_cursor`; replayed batches dedupe to zero
  jobs; dropped/irrelevant events never reach a sink.

## Acceptance

- Unit: prompt-contract snapshot assertions, validation-hint, per-source
  interval bounds, stream pacing, batch→one job, cursor persistence,
  UTF-8 byte roundtrip, catalog arg completeness, docs coverage.
- Live on `artem_main`: catalog 5.3.0 registered and pinned, prompt+tools
  projected (rebind + respawn), «следи за группами…» produces
  `watch_subscribe` with `delivery=stream` on the account-level wait tool —
  not a 15-minute agent cron. Verify a real batch wakes once and idles cost
  zero wakes.

## Out of scope

- Multi-source single-watch fan-in beyond one-args-scope subscriptions.
- `AssignmentMaxSources` policy changes (not the scaling mechanism).
- Removing the legacy `artem_two` monitor (owner-side cleanup only).

## Done checklist

- [x] Tests red then green on each slice.
- [ ] `just check` (coverage ≥85%); no dependency changes.
- [ ] docs/tools.md + docs/routines.md + AGENTS.md + engineering.md updated.
- [ ] Live acceptance on artem_main recorded in state.yaml.
