# CHG-0111 — Durable reminder acceptance (#247)

Contract: SPEC-0013 timing/recovery and SPEC-0054 R1.4; existing ADR-0041.

## Scope

Fix `SnoozeSchedule` and `TickSchedules` in the shared spool; keep HTTP,
agent tools and `/routines` consistent. Preserve owner/context checks, revisions,
outbox deduplication, configurable catch-up and the weekly cron cadence.
No dependencies, upstream changes or migration of existing user schedules.

## Regression design

- ADD reminder snoozed two hours, including after its old occurrence entered the
  outbox: restart, no early send, exact text once at the new deadline; old revision
  cannot send. Cron resumes at the deferred deadline then Friday 10:00 Moscow.
- ADD full schedule -> occurrence -> outbox recovery at the catch-up boundary,
  outside it (`missed`), and with a custom window; repeated/concurrent ticks and
  restart must preserve one logical delivery. Use explicit clock values and disk.
- ADD HTTP owner-session tests: Bob has a valid token but cannot read/change/delete
  Alice's reminder; missed and uncertain transport outcomes remain owner-visible.
- KEEP existing cancellation/revocation, spool lock and outbox idempotency tests.
- ADD gateway contract with real local HTTP adapters, unavailable runtime/model
  sentinels and a lost transport response: no agent calls and no blind resend.
  A transport double proves dispatch only; real Telegram acceptance is separate.
- ADD live-discovered backlog regression: a fresh reminder must dispatch ahead of
  expired pending history without widening the 16-settlements-per-pass limit.

## Execution

1. Write regressions; static self-review (not an independent critic); confirm red.
2. Apply the smallest shared fixes and derived delivery-state reporting.
3. Run package race tests, `just check`, Docker checks available for the changed
   image, then `hubctl dev-rebuild` for the selected local/dev gateway and health.
4. Record acceptance evidence, update current docs and this state.

## Acceptance evidence

| Issue criterion | Evidence |
|---|---|
| Exact saved text, no runtime/model at fire | `TestReminderGatewayWithoutRuntimeAndLostTransportAnswer`; real Telegram `E2E-081` |
| Restart and overdue policy | `TestReminderScheduleCatchUpAcrossRestartAndConcurrentTicks`; durable `missed` outside the window |
| Concurrent ticks and logical deduplication | 12 concurrent schedule/dispatch pairs, one occurrence/outbox delivery |
| Lost transport answer remains uncertain | Real local HTTP connection closed after receiving send; failed receipt survives restart without resend |
| Valid foreign session cannot manage the reminder | `TestReminderOwnerHTTPStatusAndIsolation`; Bob bearer remains Bob despite an Alice principal header |
| Cancellation/revocation before delivery | Gateway worker contract refuses both before any HTTP send |
| Snooze and Friday 10:00 cadence | `TestReminderSnoozeSurvivesRestartAndPreservesWeeklyCadence`; old persisted snooze supported |
| Weekly crash recovery and backlog | `TestWeeklyReminderRecoversCurrentSlotAfterExpiredPending`; `TestReminderIsNotStarvedByExpiredOccurrenceBacklog` |
| History isolation after retention | `TestReminderHistoryDoesNotExposeForeignDeliveryAfterRetention` |

The package tests and HTTP transport contracts do not replace the real Telegram
receipt. Final gate results and exact deployed image are recorded in `state.yaml`.

## Boundaries

Do not commit unrelated work. Do not publish or send external messages without
the owner's actual instruction; prepare the exact final live test first.
