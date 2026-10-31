# CHG-0199 evidence

Root-independent S199.1 implementation has started after the CHG-0198 Windows gate passed at 85.91%. Broker-dependent work remains gated by CHG-0194/0195/0196; no SQLCipher introduction or Docker work.

Initial regressions place retained synthetic bytes outside pending and verify admission refusal without deleting the source. A second regression requires maximum-next-write plus lifecycle reserve headroom. Current logs belong under ignored `.work/tmp/`; actual results follow after commands finish. No integration/live acceptance claim.

## Reproduced S199.1 regressions

`.work/tmp/storage-full-footprint-red.log`: test process exited 1, as required for red-first reproduction. Every retained-data case (outbox/blobs, outbox/done, archive, run-events, event-state, inbound, tmp, backups) admitted a new job despite 64 KiB of owned retained data exceeding the 32 KiB synthetic ceiling. The 64 KiB case with 45 KiB retained data also admitted without 8 KiB maximum-next-write and 16 KiB lifecycle headroom. Retained sources remained intact.

Current repository includes these intentionally failing regression tests. The prior root PASS at 85.91% is the CHG-0198 snapshot before them, not a green claim for current S199.1. Next: replace pending-only admission accounting with bounded owner counters covering metadata, outbox, archive, recovery, artifact and scratch writes; reconcile after crash and periodically, enforce file cap/next-write reserve, then fix owner maintenance and scoped operator CLI. Broker-dependent accounting remains gated; no Docker or actual data cleanup.


## Communication accounting implementation, 2026-10-29

The root-owned counter now reconstructs logical bytes and bounded filesystem entries at startup, updates totals on writes/renames/removals, and reconciles on dirty state or after 30 seconds. Cached snapshots never recursively walk. Admission reserves maximum-next-write headroom plus a separate lifecycle byte/entry reserve. Atomic replacement scratch, archived gzip scratch, append growth, inbound payloads and media staging are covered. Configured mock-sent/mock-links share the same owner budget. Errors and snapshots expose no owner filenames. Logical size is not allocated disk usage.

Observed terminal results:
- `storagebudget-current-race.log`: counter race PASS, 81.4% coverage at that intermediate snapshot.
- `storage-full-footprint-initial-green.log`: original footprint/reserve regressions PASS after initial accounting integration.
- `storage-writers-compile.log`: selected communication usage/mock/ceiling tests PASS.
- `storage-stream-red.log`: oversized provider stream reproduced a Windows scratch leak because removal preceded file close; live-write/lifecycle-reserve regression already passed.
- `storage-writers-race.log`: selected communication plus counter race PASS after closing before cleanup, reserving bounded stream capacity, and enforcing writes at the stream boundary.
- `storage-blob-cleanup-red.log`: failed completion deleted an unsettled blob. Cleanup now follows successful durable completion and targets only blobs named in that delivery; unknown files are retained. Full owner race verification is in progress.

All fixtures use temporary synthetic files. No Docker/container/native-volume acceptance or working-data migration was performed. S199.1 remains in progress: ToolHub accounting, configuration review and complete owner checks are outstanding. S199.2 has begun; S199.3 has not started. The current root gate result is recorded below.

## Current root gate and fail-closed correction

`chg0199-accounting-final-root-check.log`: `just check` exited 1. Its Go tests, docs/config checks and coverage completed; own Go statement coverage was 85.93%. The failing subgate was gosec G703 at `removeOwned`'s no-counter fallback. A synthetic regression reproduced that an unbound Spool deleted a retained file. The fallback now returns a fixed unavailable error without touching it. The targeted Communication/hubctl race tests and standalone `go run ./cmd/devcheck gosec` both exited 0 after the fix. The full `just check` has not been rerun after that fix and is **not claimed green**.

The first combined owner-maintenance change was rejected by automatic review because its byte/file branches lacked separately observed failing assertions. It was removed. The replacement below was built from separate red/green tests. No live or Docker verification was performed.

`chg0199-accounting-post-fix-root-check.log`: the subsequent full `just check` exited 0 with 85.93% own Go statement coverage. This verifies the current root tree only; it does not constitute Broker SQLCipher/native or container acceptance. S199.1 remains in progress because ToolHub/Broker full-owner accounting and the remaining owner checks are not implemented.

## Communication owner maintenance

Synthetic filesystem regressions separately reproduced lost `Compact` errors, an early normal retry, a missing default retention fallback, and missing five-minute scheduling at 80% of byte and file limits. The control-loop test reproduced that a failed `Compact` remained invisible. Each failed before its corresponding change. The loop now retains last attempt/success and a fixed `cleanup_failed` code in memory, retries failures after five minutes, runs hourly normally, and shortens the interval at the high-water threshold. `Compact` still applies its existing protected-state predicates and retention; there is no new deletion target.

`chg0199-maintenance-communication-race.log`: first full Communication race attempt exited 1 solely at existing `TestHermesRunnerSuccess` (helper invocation failed during the broad run). That test and all new maintenance tests passed together on an isolated race repeat. The first failed run is retained as evidence; its result is not counted as a pass.

`chg0199-maintenance-communication-race-repeat.log`: second full Communication race run exited 0. `chg0199-maintenance-root-check.log`: subsequent full `just check` exited 0 with 85.94% own Go statement coverage, including docs, lint and package tests. This is root synthetic evidence, not Docker, Broker native or live acceptance. Communication owner maintenance is implemented; ToolHub/Broker owner work and S199.3 remain.

## ToolHub accounting, owner maintenance and operator CLI, 2026-10-30

Synthetic regressions reproduced, then fixed one by one (probity-enforced red-first):

- `TestToolHubSaveFailsClosedWhenStateBudgetExhausted`: a persisted baseline is loaded through `Load`, then a bounded onboarding update grows the snapshot past the owner byte budget while staying under the next-write cap. `Save` refused and the prior snapshot plus in-memory mutation stayed intact. The storage counter now binds lazily to the whole persisted-store directory (`internal/toolhub/storage_limits.go`), so `store.json` and owned sidecar state share one budget; `saveLocked` wraps the atomic tmp→fsync→rename sequence in `counter.Write`.
- ToolHub maintenance regression: `maintainStorageLocked` reconciles the counter after a successful snapshot write, records last attempt/success and a fixed failure class in memory, and backs off for one hour after a failed reconcile instead of hammering the filesystem. A protected-live-state test confirms `compactLocked` semantics retain current/pinned records.
- `hubctl storage status` (`cmd/hubctl/storage.go` + dispatch): emits only allowlisted per-owner aggregates (`owner`, `status`, `logical_bytes`, `files`, `limit_bytes`, `limit_files`, `complete`) for `communication-spool` and `toolhub-state`. A regression asserts paths, file names and object names never appear in output; a wiring test reaches the surface through `run()`.
- `hubctl storage cleanup`: dry-run emits a digest-bound plan (`scope`, `digest`, aggregate `files`/`bytes` — never candidate names) from `Spool.StoragePlan` (`internal/communication/storage_plan.go`), which rejects planted symlinks and excludes the `.writer.lock` protocol file from the fingerprint. `--apply --digest` takes `communication.LockSpool`, recomputes the plan, returns `ErrStalePlan` on stale/foreign digest, then runs the bounded `Compact` retention pass. Regressions cover: stale digest after content drift, foreign digest, symlink escape on the plan path, verified apply archiving an aged done record into `archive/` while preserving live state, and dispatch wiring (dry-run default, apply without digest refused, foreign digest refused as stale).

Terminal results on this tree: `go test ./cmd/hubctl -run 'TestStorageStatus|TestCleanup'` PASS (all 6 tests); `go test ./internal/toolhub ./internal/toolhub/api ./internal/storagebudget` PASS; `go test ./internal/communication` reported one failure in existing `TestWorkerRunsWithIsolatedUserAndHandlesError` ("worker did not fail job", 2 s poll deadline) during the heavily loaded full-package run — the same test passes 3/3 in isolated repeat (~0.2 s each), matching the previously documented Windows timing/file-lock flake class. An isolated full-package repeat is running; the failure is recorded, not counted as green.

Fixture note: three pre-existing OAuth confirmation tests failed with `trusted human confirmation failed: 403` because their onboarding fixtures lacked the `confirm` control-operation grant; `grantTestControlOperations(..., "confirm")` added in `internal/toolhub/m13_p2_test.go` and `internal/toolhub/prepared_oauth_test.go`, after which all pass.

Docs: `docs/services/hubctl.md` gained the storage operator-surface section and command-list entry; `docs/operations.md` documents `storage status`/`cleanup` under the operator ops surface. Not verified: Docker/native durability, Broker owner surfaces (gated by CHG-0194), live acceptance.

Repeat: the isolated full-package `go test ./internal/communication -count=1` rerun exited 0 (106.7 s), confirming the earlier `TestWorkerRunsWithIsolatedUserAndHandlesError` failure as a load-timing flake, not a regression in the storage wrappers.
