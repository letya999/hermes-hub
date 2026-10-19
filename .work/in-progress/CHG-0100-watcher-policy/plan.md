# CHG-0100: Watcher notification policy (M12 stage 2)

Chain: `docs/adr/ADR-0041-proactive-automation.md` D3 -> `specs/active/SPEC-0054-proactive-automation.md` R2
-> this plan -> `state.yaml`. Evidence: `../CHG-0099-followup-hardening/research.md`.
Previous stage: `CHG-0099`. Next stage: `CHG-0101`. GitHub issue: #248.

## Goal

Watchers/checks/events respect the owner's notification policy: quiet hours
defer deliveries instead of firing at night, a daily cap bounds noise, and
`notify_mode=agent` escalates only genuinely new revisions to the model.

## Read before coding

1. SPEC-0054 R2, ADR-0041 D3.
2. `internal/communication/watchers.go` `settleProbe`;
   `internal/communication/communication.go` `Delivery`/`ClaimDelivery`;
   `internal/communication/occurrences.go` agent-kind admission branch.

## Slices

### S2.1 Quiet window + Delivery.not_before (R2.1)

Anchors: `schedules.go` `Schedule`/`validateScheduleDraft`;
`communication.go` `Delivery`, `ClaimDelivery`;
`watchers.go` `settleProbe` delivery construction.

Change:
1. `Schedule` gains `QuietFrom`, `QuietTo string` (HH:MM, both-or-neither,
   validated against the schedule timezone).
2. `Delivery` gains `NotBefore time.Time`; `ClaimDelivery` skips entries
   whose `not_before` is after now (they stay pending — durable).
3. Helper `quietDefer(schedule, now) time.Time`: zero outside the window;
   inside -> the next window end in the owner tz (overnight wraps).
4. `settleProbe` (and stage-5 event deliveries, digest) set `NotBefore`
   via `quietDefer` on every owner-facing delivery.

Tests:
- `TestQuietHoursDeferDelivery`: probe change inside window -> delivery
  exists with `not_before` = window end; `ClaimDelivery` returns nil before,
  the delivery after.
- `TestQuietOvernightWraps`: 22:00-07:00 defers 23:30 to 07:00.
- `TestQuietValidation`: one-sided window rejected.

### S2.2 Daily notification cap (R2.2)

Anchors: `watchers.go` `WatchState`, `settleProbe`.

Change:
1. `Schedule` gains `DailyLimit int` (0 = unlimited, cap 32).
2. `WatchState` gains `NotifyDay string` (YYYY-MM-DD owner tz) +
   `NotifyCount int`.
3. In `settleProbe` notify branch: over limit -> `live.State = "capped"`,
   no delivery, state still advances; new day resets the counter.
   `digest`-mode watchers are exempt (they aggregate by design).

Tests:
- `TestDailyLimitCaps`: limit 1 -> first change delivers, second settles
  `capped`, third next-day delivers again.
- `TestCappedVisibleInState`: occurrence file reads `capped`.

### S2.3 `notify_mode=agent` escalation (R2.3)

Anchors: `schedules.go` `validateScheduleDraft` NotifyMode check;
`occurrences.go` agent admission (the `else` branch);
`watchers.go` `settleProbe`.

Change:
1. `notify_mode` accepts `agent` (watcher/check only).
2. In `settleProbe`, when the predicate fires and mode is `agent`: instead
   of a Delivery, enqueue `o.Job` as an agent job (same admission as the
   agent branch: `enqueueLocked` with `routineConversation`), text =
   `"[untrusted watcher evidence] " + input + was/now`. Occurrence ->
   `dispatched`.
3. Idempotency: the admitted job keeps `o.Job.ID`/`IdempotencyKey`, so a
   re-probe after a crash cannot double-admit; `active[key]` guard already
   serializes one in-flight occurrence per schedule.

Tests:
- `TestAgentEscalationOnChange`: change -> one spool job, zero deliveries,
  text flagged untrusted; re-run same revision -> no second job.
- `TestAgentModeNoChangeNoJob`: unchanged probe -> `no_change`, jobs dir
  empty.

## Red-team checklist

- `not_before` must survive restart (durable field, not memory).
- Daily limit counts in the owner timezone, not UTC.
- `notify_mode=agent` on reminder/script/digest is rejected (watcher/check
  only) — a reminder must stay LLM-free.
- Quiet window defers, never drops: outbox pending entry is auditable.

## Acceptance

SPEC-0054 A4. Live: create a watcher with quiet window covering now on the
dev stack, force a change, observe the pending delivery fire at window end.

## Out of scope

- Per-channel quiet policies; connector sources (stage 3).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team + live acceptance in state.yaml.
- [ ] Issue #248 commented.
