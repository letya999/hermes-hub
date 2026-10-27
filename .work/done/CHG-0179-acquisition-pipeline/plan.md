# CHG-0179: shared acquisition pipeline (M23 stage 2)

Chain: `docs/adr/ADR-0082-standing-assignments.md` D5–D7 ->
`specs/active/SPEC-0081-shared-acquisition.md` R1–R2 -> this plan ->
`state.yaml`.
Previous stage: `CHG-0178`. Next stage: `CHG-0180`, `CHG-0181`.

## Goal

Two subscriptions that poll the same source pay one fetch inside their
security boundary; HTTP pulls honor conditional requests, Retry-After and
jittered dues; connector cursors and their gaps are durable and visible;
events pass a deterministic keep/drop/urgent filter before any sink; push
sources land through a token-gated hook endpoint — all without weakening a
single global routine bound.

## Read before coding

1. `internal/routines/watchers.go` `probeOne`, `SettleProbe`,
   `LoadWatchStateLocked`; `internal/routines/schedules.go`
   `settleScheduleLocked`, `createSchedule`; `internal/routines/validate.go`.
2. `internal/communication/{watchers,events,streams,http}.go` —
   `fetchWatch`, `toolCallProbeWithin`, `serviceProbe`, `settleEvents`,
   `fanoutEventsLocked`, `streamPoll`, route registration + auth.
3. `internal/commodel/routine.go` `WatchProbe`, `RoutineOccurrence`;
   `internal/communication/scriptrun.go` (`runProbeScript` shape).
4. `research.md` — iva-agent/goclaw cursor + Retry-After patterns.

## Preconditions and rules

- No global limit weakening; sub-minute stays rejected.
- Sharing never crosses envelope boundary; bodies stay ≤128 KiB shared.
- fetch dep signature change is internal-only; update all callers/tests.

## Slices

### S2.1 Acquisition pool + conditional fetch + backoff/jitter (R1.1–R1.5)

Anchors: new `internal/commodel/acquisition.go`;
new `internal/routines/acquisitions.go`;
`internal/routines/watchers.go` `probeOne` (dispatch wrap);
`internal/communication/watchers.go` `fetchWatch`;
`internal/routines/schedules.go` `settleScheduleLocked`, `createSchedule`;
`internal/communication/gateway_toolhub.go` `toolCallProbeWithin`.

Change:
1. `commodel.Acquisition` + `AcquisitionKey(boundary, probe)` canonical
   JSON of `{kind,url|tool,args(sorted)}`.
2. Engine: `acqPath`, `loadAcquisitionLocked`, `joinAcquisitionLocked` at
   create (after schedule persist), `leaveAcquisitionLocked` at delete
   (last member ⇒ remove file); store `interval_sec` on schedule at
   create (expression spacing: `every:Ns`→N; cron→median of next two
   occurrences; `once:`→0 = never share).
3. `probeOne` http/mcp_tool/service/render dispatch →
   `acquireLocked(...)`: hit fresh snapshot (age < interval_sec) ⇒ use
   stored body, `shared_hits++`; miss ⇒ fetch, store bounded body +
   headers; not shareable (kind command/script/task/hook, body>128KiB)
   ⇒ passthrough.
4. `fetch` dep → returns `FetchResult{Body, ETag, LastModified,
   NotModified, RetryAfterSec}`; `fetchWatch` sends stored conditionals
   (passed via ctx or an acquisition-aware fetch wrapper — engine hands
   headers down); 304 ⇒ `SettleProbe` with previous value hash ⇒
   `no_change`.
   `toolCall`/`serviceCall` keep byte bodies (no conditionals).
5. `WatchState` += `backoff_until`; `SettleProbe` honors
   `FetchResult.RetryAfterSec`/auth-failure streak (exp×2 cap 15m);
   `probeOne` early-skips with occurrence `retry_after` set.
6. `settleScheduleLocked`: next `DueAt` += deterministic jitter
   `hash(scheduleID) % (0.05×interval)` cap 60s for watcher/check/event.

Tests:
- `TestAcquisitionSharesWithinBoundary` (A1.1)
- `TestAcquisitionNeverCrossesBoundary` (A1.2)
- `TestConditionalHTTP304` (A1.3)
- `TestRetryAfterBackoff` (A1.4)
- `TestAcquisitionMembershipLifecycle` (last-out deletes file)
- `TestOversizedBodyNotShared`, `TestJitterDeterministic`

### S2.2 Cursors + gap surfacing (R1.6)

Anchors: `internal/communication/events.go` `EventState`,
`extractEvents`, `settleEvents`; `internal/communication/streams.go`
`streamPoll` (envelope parse shared with poll path).

Change:
1. `extractEvents` (or a sibling `parseEventEnvelope`) also surfaces
   `cursor`, `next_cursor`, `cursor_expired`; `EventState` +=
   `cursor`, `gap_since`, `gap_reason`.
2. `settleEvents`: persist cursor on success; `cursor_expired` ⇒
   `gap_since=now` + `notifyProbeFailureLocked`-style deduped owner
   notice ("source cursor expired — possible missed events"); next
   success clears `gap_since`.
3. `routine_get` event_state output gains `cursor`/`gap_since` (find the
   card builder in `routine_management.go`).

Tests:
- `TestCursorAndGap` (A1.5), `TestCursorGapNoticeOnce`

### S2.3 Event filter script (R2.1–R2.4)

Anchors: `internal/commodel/routine.go` `WatchProbe.Filter`;
`internal/routines/validate.go` `validateProbe*` (filter: event-only,
syntax-check); `internal/communication/events.go` `fanoutEventsLocked`;
`internal/communication/scriptrun.go` + `gateway.go` `probeScript`
(filtration mode).

Change:
1. `WatchProbe.Filter string`; validate: only `job_kind=event`, ≤8 KiB,
   compiles, `needs`≤2 allowed for filters.
2. `Deps.EventFilter func(ctx, eventJSON, src, needs)` on engine→spool;
   spool impl runs same isolated script runtime, mode "filter",
   contract `emit("keep"|"drop"|"urgent"[, reason])`. Runtime-error ⇒
   treat as keep + record `last_error` (fail-open delivery, fail-closed
   authz stays untouched).
3. `fanoutEventsLocked`: unseen → filter → `drop` marks seen + counts
   `filtered_out`; `urgent` sets `NotBefore=now` and `[urgent]` prefix
   (sink=notify) / bypasses quiet defer (sink=agent). Runtime-unavailable
   ⇒ keep path.
4. Filter text participates in `ProbeRevision` (any probe change bumps it
   → seen-set resets — existing machinery).

Tests:
- `TestEventFilterVerdicts` (A2.1)
- `TestFilterRevisionResetsSeen` (A2.2)
- `TestFilterRuntimeDownKeeps`, `TestFilterOnWatcherRejected`

### S2.4 Hook intake endpoint (R1.7)

Anchors: `internal/commodel/routine.go` probe kind `hook`;
`internal/routines/validate.go`;
`internal/communication/http.go` mux + auth seam;
new `internal/communication/hooks.go`;
`internal/communication/routine_management.go` create path (token mint +
one-time return).

Change:
1. `kind=hook` valid only with `job_kind=event`; no url/tool required;
   `watch_subscribe`-style create mints `crypto/rand` 16B token → stores
   `schedule.HookTokenSHA`; create response includes `hook_url`
   (path + token) once.
2. `POST /v1/hooks/{id}`: no session auth — the token *is* the
   credential; constant-time compare sha256(provided) vs stored; body
   ≤256 KiB; `{events:[…]}|array|object` → `extractEvents`-equivalent →
   filter (S2.3) → `fanoutEventsLocked` with seed
   `hook-<sched>-<bodyhash>`; 200 always on acceptance shape, 404 on bad
   token/id, 410 on paused/deleted.
3. Event ids: explicit `id` honored; else content hash — replayed batches
   dedupe via seen-set.

Tests:
- `TestHookIntake` (A1.6) incl. wrong-token 404, paused 410, replay no-op,
  body-bound rejection.

## Red-team checklist

- Cross-principal same-probe ⇒ separate acquisitions (A1.2).
- Stolen hook token ⇒ only that subscription affected; rotate = recreate
  (document); token never logged (bounded log fields only).
- `Retry-After: <absurd>` ⇒ clamp 1 h; failure streak cap 15 min.
- Filter emitting garbage ⇒ treated as keep once, error recorded.
- Cursor poisoning (empty string) ⇒ ignored; only non-empty cursor stored.
- 304 after body actually changed upstream-without-etag-change is a
  provider contract bug; documented limitation, not hidden.

## Acceptance

SPEC-0081 A1.1–A1.6, A2.1–A2.2 green; `just check`; coverage ≥85% on
touched packages.

## Out of scope

- Assignment lifecycle (CHG-0178 already landed fences this consumes),
  REST/agent/chat surfaces (CHG-0180), live multi-source run (CHG-0181).
- WebSocket/SSE pull transports — stream kind stays the existing MCP
  long-poll; generic SSE is future work, not this milestone.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml slices/validation/evidence updated.
- [ ] Commit `CHG-0179` slices.
