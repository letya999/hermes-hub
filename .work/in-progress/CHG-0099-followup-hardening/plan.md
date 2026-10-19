# CHG-0099: Verified follow-up hardening (M12 stage 1)

Chain: `docs/adr/ADR-0041-proactive-automation.md` D2 -> `specs/active/SPEC-0054-proactive-automation.md` R1
-> this plan -> `state.yaml`. Evidence: `research.md`.
Next stage: `CHG-0100`. GitHub issues: #251 (predicate follow-ups), #247 (reminder evidence).

## Goal

Conditional follow-ups become honest about `unknown`: a `check` retries a
failed probe a bounded number of times, its delivery says why it fired, and a
new `task:<id>` probe verifies hub task state without any URL. The #247
reminder contract is evidenced, not rebuilt — it already exists.

## Read before coding

1. `specs/active/SPEC-0054-proactive-automation.md` R1, ADR-0041 D2.
2. `internal/communication/watchers.go` `settleProbe`/`probeOne`;
   `internal/communication/schedules.go` `validateScheduleDraft`;
   `internal/communication/tasks.go` `loadTaskLocked`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only.
- `just check` before handoff.

## Slices

### S1.1 Bounded probe retry for check (R1.1)

Anchors: `internal/communication/occurrences.go` `RoutineOccurrence`;
`internal/communication/watchers.go` `settleProbe`;
`internal/communication/schedules.go` `Schedule`.

Change:
1. `RoutineOccurrence` gains `RetryCount int`; `Schedule` gains
   `MaxRetries int` (json `max_retries`, 0 = default 3, cap 8, valid only on
   `check`).
2. In `settleProbe`, on `probeErr != nil` and `o.JobKind == "check"` and
   `o.RetryCount < limit`: set `live.State = "pending"`,
   `live.DueAt = now.Add(time.Duration(live.RetryCount+1) * time.Minute)`,
   `live.RetryCount++` — the ordinary dispatcher re-probes it. Exhaustion or
   other kinds keep `failed`.
3. `validateScheduleDraft` rejects `max_retries` outside 0..8 or on non-check
   kinds.

Tests (red first):
- `TestCheckProbeErrorRetriesThenFails`: failing probe -> occurrence pending
  with bumped due; after max retries -> failed.
- `TestCheckRetryNotForeignKind`: watcher/reminder probe error settles
  `failed` immediately.

### S1.2 Check verdict in delivered text (R1.2)

Anchors: `internal/communication/watchers.go` `settleProbe` notify branch.

Change:
1. For `check` the delivered text becomes
   `fmt.Sprintf("%s\ncheck %s met\nwas: %s\nnow: %s", input, expect, prev, cur)`
   (bounded via `boundedWatchText`).

Tests:
- `TestCheckDeliveryNamesVerdict`: equals: predicate delivery contains the
  expect string and both values.

### S1.3 `task:` probe kind (R1.3)

Anchors: `internal/communication/schedules.go` `validateScheduleDraft`
check/watcher URL validation; `internal/communication/watchers.go`
`probeOne`/`probeValue`; `internal/communication/tasks.go` `loadTaskLocked`.

Change:
1. `probe.url` may be `task:<id>` where `<id>` matches `spoolIDPattern`
   (accept for `check` and `watcher`; reject regex with it).
2. `probeOne`/`probeValue` resolve `task:` values by
   `s.loadTaskLocked(o.Job.PrincipalID, id)` -> `open`/`closed`/`archived`
   string; a load error is a probe error (R1.1 retry then `failed`).
3. Baseline seeding in `handleRoutines` for `task:` reads the same predicate
   (not `fetchWatch`).

Tests:
- `TestCheckTaskProbeFiresOnClose`: task open at create -> still open ->
  skipped; task closed -> dispatched once.
- `TestCheckTaskProbeForeignTaskFails`: schedule for B's task id under A's
  envelope -> probe error -> failed after retries, never dispatched.

### S1.4 Reminder contract evidence (#247)

Anchors: existing tests in `occurrences_test.go`, `stabilization_test.go`;
`docs/architecture.md` routines section.

Change:
1. Add one focused test asserting a due reminder enqueues a delivery with no
  job admission (spool jobs dir stays empty) — the no-model/no-runtime proof.
2. One paragraph in `docs/architecture.md` naming the overdue policy
  (catch_up_window then `missed`) and the uncertain-delivery status.

Tests:
- `TestReminderFiresWithoutRuntime`.

## Red-team checklist

- `task:` id of another principal -> probe error path, never a state value.
- `retry_count` cannot grow past `max_retries`; a zero `max_retries` uses 3.
- A `task:` URL never reaches `fetchWatch` — no network call for it.
- Snooze/pause during retry: `occurrenceCurrentLocked` still cancels stale
  revisions.

## Acceptance

SPEC-0054 A1 (reminder delivers with runtime down), A3 (task predicate
fires once, skipped while false, foreign task fails closed).

## Out of scope

- Semantic/unverifiable conditions (#251 explicitly defers them).
- Connector-state predicates — stage 3 (`mcp_tool`).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team recorded in state.yaml.
- [ ] docs/architecture.md updated.
- [ ] Issue #251/#247 commented with evidence.
