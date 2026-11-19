# CHG-0333: live-stack wedges and lost updates

Issue #598, M40.0. Branch: fix/live-wedges from origin/dev. No service extraction.

## Scope and evidence

| Item | Regression | Fix commit |
|---|---|---|
| 1 uncertain lane and operator resolve | TestUncertainJobReleasesLaneAfterSupervisorSettles | 036f5a1b, 56106e46 |
| 2 count derived from lease rows | TestLeaseCountDerivedFromLeaseRows | 71942292 |
| 3 overdue running job | TestRunningJobPastDeadlineBecomesUncertain | 81e41796 |
| 4 revised pending event batch | TestScheduleEditDropsPendingBatchAndKeepsOtherStreams | c0c133e3 |
| 5 claimed mapping corruption | TestCorruptMappingSettlesClaimedJob | b3099d1f |
| 6 goal clear during evaluation | TestGoalClearDuringEvaluationIsNotUndone | b4300ca2 |
| 7 failed dreaming night | TestFailedDreamingNightDoesNotBlockNextNight | c1f65533 |
| 8 SQLite binding migration | TestRenderMigratesBindingsOnSQLiteStore | 6b4c7723 |
| 9 first apply without actions | TestFirstApplyCrashRecoversWithoutIntervention | 5d0b49bb |
| 10 paused assignment | TestApplyRefusesPausedAssignment | 503884fe |

Lease rows now own every hold, including anonymous Ensure references. Acquire
persists the row with the runtime state. Release and restart checks derive demand
from rows; public diagnostics use Consumers. A restarted supervisor sweeps an
expired orphan hold. Operator resolve is documented in the hubctl and communication
service guides and uses all four locale catalogs.

## Validation

Owner handoff records behavioral red/green for items 1 and 3-10. The continuation
reproduced item 2 red on 56106e46: Ensure left consumers empty; green after
71942292. No saved transcripts or personal state are used for these tests.
Run just check and just test on routines, toolhub/automation, toolhub, stack,
migration, cmd/hubctl, communication and supervisor. Keep Probity enabled.

## Owner decisions and limits

- Item 10 refuses assignment_edit for paused assignments. Decide whether this
  semantic correction requires the full agent-tool contract release procedure.
- Item 7 fails after three unsuccessful attempts; an already prepared digest is
  not delivered after the occurrence is failed.
- Item 4 drops a pending batch on any revision change, including probe changes.
- Outside scope: governance.go, grant.go and secret.go still default to store.json
  after the SQLite cutover.
- Live smoke, push and PR require an explicit owner instruction. No remote
  acceptance exists for these unpublished commits. Worktrees are retained.
