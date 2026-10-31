# CHG-0188: M23 live-wake reliability fixes

Chain: `docs/adr/ADR-0082-standing-assignments.md` -> `specs/active/SPEC-0080`,
`specs/active/SPEC-0081` -> this plan -> `state.yaml`.
Branch: `fix/m23-live-wake`. Preceded by CHG-0178..0181 (M23 plane,
pipeline, control surface, acceptance).

## Context — the incident

A proactive assignment created from Telegram produced no notification.
Live-stack audit found three breaks:

1. Runtime `artem_two/gen-425` was spawned from a stale supervisor pin
   carrying an old `policy_version`; every `/v1/execute` was rejected in
   0 ms and classified `uncertain`, producing endless `/v1/resume` churn.
2. The first `-error` owner notice was blocked (stale epoch) and died in
   `outbox/failed`, yet `RecordRoutineFailure` had already written the
   dedupe digest at enqueue — ~117 subsequent identical failures stayed
   silent.
3. The agent could not create an assignment at all: `assignmentCall` was a
   stub that always returned `{"status":"ok"}` (no `X-Hub-Principal`, no
   status-code check, no reply decode), and the `assignment_*` tools were
   absent from the projected ToolHub catalog (immutable `5.0.1` has 50
   tools) and from the direct MCP server — so the agent fell back to a
   plain event routine with a malformed filter.

Secondary defects confirmed by audit: filter scripts ran under the spool
lock and failed open; `extractEvents` could not read `results`/`messages`
envelopes or numeric ids; `sink=agent` bypassed the daily limit, quiet
hours and the assignment wake budget; `watches`/`assignments` were not
ToolHub capability families; the holds fetch ran under `s.mu`.

## P0 — live remediation (done on the dev stack)

- Paused the 8 `tg-gh-*` schedules; removed the stale pin; respawned the
  runtime as `gen-426` with policy `cb2f61ab`; patched all 8 schedules'
  filters to compiling Starlark (`any([...])`, fixed corrupted Cyrillic).

## Slices

### S1 Supervisor generation/policy fencing

- `Runtime.PolicyVersion` recorded at spawn; `Ensure` replaces a live
  runtime whose pinned policy differs from the current policy;
  `ReconcilePins` restamps stale pins before respawn; runtime 409
  rejection settles the run as terminal `failed`, not `uncertain`.
- Tests: `policy_replacement_test.go`, `phases_test.go`,
  `http_rejection_test.go` (409 row now expects `failed`).

### S2 Failure-notice dedupe on confirmed delivery

- `commodel.Schedule.PendingFailureSHA256`: enqueue-time in-flight marker;
  `LastFailureSHA256` is recorded only when the notice is actually
  delivered. `MarkRoutineFailurePending`/`ReleaseRoutineFailurePending`/
  `RecordRoutineFailure`/`ClearRoutineFailure` own the two-phase state;
  `Gateway.uncertainDelivery` releases the pending marker only while it
  still names the dead notice's failure.
- Covers `-error`/`-uncertain` job notices and `watch-fail-*` probe
  notices. Tests: `policy_test.go` (watch-fail dedupe waits for
  delivery), `routine_isolation_test.go` (undelivered notice retries),
  `watchers_test.go` (repeat stays silent, recovery re-arms).

### S3 Event pipeline correctness

- `extractEvents`: `results`/`messages` envelopes, bare arrays, numeric
  ids, stable content-hash fallback, `next_cursor` persisted after
  settlement, `{"event":false}` timeout sentinel stays a no-op.
- `Probe.Filter` validated at schedule creation through the script
  compiler (`probe filter does not compile`).
- `settleEvents`/`settleStreamEvents`: validate+extract under `s.mu`,
  filter scripts outside the lock, revalidate occurrence/schedule/seen-set
  under the lock before fan-out; filter errors fail closed (occurrence
  `failed`, `LastError` persisted, events stay unseen).
- `fanoutEventsLocked` agent sink: quiet-window suppression, daily limit,
  `AdmitAssignmentWakeLocked` (assignment must be `active` and under its
  per-owner-day wake budget; `WakeDay` rollover resets `WakesToday`),
  `LastSuccess`/`NotifyCount` updates.

### S4 Assignment tool delivery

- `assignmentCall`: `X-Hub-Principal`, `Content-Type`, non-2xx surfaces as
  error, reply decoded into `result`.
- Catalog `hub-agent-tools` bumped to `5.2.0`: `5.1.0` was already immutable
  in the live store, and the projectability test exposed duplicate
  `capability_id`s — a managed profile selects one tool per
  `(capability_id, connection)`, so shared ids are unprojectable. All ten
  `assignment_*` tools got unique `assignments.*` ids; pre-existing
  collisions fixed the same way (`routine_get` -> `routines.get`,
  `images_inspect` -> `images.inspect`).
- `assignment_*` registered on the direct MCP `Server()` — same executor
  semantics as the managed ToolHub projection.
- `assignments` becomes a `via: toolhub` family (`toolhubFamilies`,
  `toolhubCapabilityID`, `communication-hub` egress, `hub:assignments`
  inventory section).

### S5 Hold projection without spool-lock I/O

- `Spool.refreshHolds` (fetch at the public enqueue boundary) +
  `holdsSnapshot` (in-lock read); active hold suppresses admission,
  settled hold stamps `OperationalEpoch`. Baked policy attestations on
  durable jobs are preserved (no restamping). `policyResolver` draft
  reverted.
- Tests: `enqueue_epoch_test.go` (epoch stamping, baked policy, active
  hold suppression, hold fetch outside the lock).

## Validation

- `go test` on communication/routines/supervisor/agenttools/stack —
  green.
- `just check` green, coverage 86.10% (first run hit a known-timing flake
  in `internal/runtime` `TestSurfaceProbeHubChildDiesOnInit`; passes 3/3
  standalone and in the rerun).
- Live acceptance done on the dev stack:
  - `hub-agent-tools@5.2.0` registered; binding + policy rev 7 + profile
    rev 9 for `artem_two` under derived policy
    `policy-a65cfdeb88f36f3c`; preview `admitted: 39/39, denied: []`.
  - Live MCP `tools/list` (the same `GET /v1/connectors` path the runtime
    uses) exposes all 10 `assignment_*` tools plus `routine_get` and
    `images_inspect`.
  - `assignment_create` -> `assignment_plan` -> `assignment_apply` via
    REST: `asg-e36fbe3c` active, source schedule
    `asg-...-inbox` applied with durable receipt.
  - Hook `POST /v1/hooks/<schedule>` 202 -> watch_state hash advanced ->
    changed payload -> real `telegram-sent` (chat_id 480637186,
    text_len 149).
  - `assignment_cancel`: state `cancelled`, `cancelled_at` stamped,
    schedule `deleted`.

## Follow-ups flagged (not in scope)

- Rotate secrets printed into a previous session transcript
  (`HUB_COMMUNICATION_AUTH`, `HUB_MEDIA_AUTH`, `OPENAI_API_KEY`,
  `KEENABLE_API_KEY` for `spaces/artem_two`).
- Clean up the test `tg-gh-*` schedules on the dev stack.
