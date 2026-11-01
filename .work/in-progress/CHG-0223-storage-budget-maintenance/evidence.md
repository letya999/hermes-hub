# CHG-0223 evidence

Root-independent S199.1 implementation has started after the CHG-0222 Windows gate passed at 85.91%. Broker-dependent work remains gated by CHG-0218/0195/0196; no SQLCipher introduction or Docker work.

Initial regressions place retained synthetic bytes outside pending and verify admission refusal without deleting the source. A second regression requires maximum-next-write plus lifecycle reserve headroom. Current logs belong under ignored `.work/tmp/`; actual results follow after commands finish. No integration/live acceptance claim.

## Reproduced S199.1 regressions

`.work/tmp/storage-full-footprint-red.log`: test process exited 1, as required for red-first reproduction. Every retained-data case (outbox/blobs, outbox/done, archive, run-events, event-state, inbound, tmp, backups) admitted a new job despite 64 KiB of owned retained data exceeding the 32 KiB synthetic ceiling. The 64 KiB case with 45 KiB retained data also admitted without 8 KiB maximum-next-write and 16 KiB lifecycle headroom. Retained sources remained intact.

Current repository includes these intentionally failing regression tests. The prior root PASS at 85.91% is the CHG-0222 snapshot before them, not a green claim for current S199.1. Next: replace pending-only admission accounting with bounded owner counters covering metadata, outbox, archive, recovery, artifact and scratch writes; reconcile after crash and periodically, enforce file cap/next-write reserve, then fix owner maintenance and scoped operator CLI. Broker-dependent accounting remains gated; no Docker or actual data cleanup.


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

Docs: `docs/services/hubctl.md` gained the storage operator-surface section and command-list entry; `docs/operations.md` documents `storage status`/`cleanup` under the operator ops surface. Not verified: Docker/native durability, Broker owner surfaces (gated by CHG-0218), live acceptance.

Repeat: the isolated full-package `go test ./internal/communication -count=1` rerun exited 0 (106.7 s), confirming the earlier `TestWorkerRunsWithIsolatedUserAndHandlesError` failure as a load-timing flake, not a regression in the storage wrappers.

## Broker owner maintenance + records rotation, 2026-11-02

SQLCipher-image evidence (container `hermes-broker-sqlcipher:test`, `-tags=libsqlite3`, real codec — not host-skipped):

- `TestRecordsRotateArchivesAndShrinks`: `RetainRecords(keep)` exports the records prefix into `records-<lo>-<hi>.seal` + HMAC-sealed manifest, verifies the archive standalone (internal hash chain re-verified), then under the destructive policy deletes the prefix across both legs in one transaction, re-anchors `records_floor`/`records_floor_prev` and VACUUMs — the test asserts the `.db` file is physically smaller after the pass and that reopen replays from the floor cleanly.
- `TestRecordsFloorAboveHeadRefusesOpen`: reopen with a witness head below the floor fails `ErrUnavailable`; a wiped head (seq=0) on a rotated store fails the same way — the guard predates the fix and panicked on negative slice indexing before it.
- `TestSQLCipherAppendBound` (rewritten contract): ledger-cap overflow answers `ErrFull` — the journal stays `Healthy()`, no poison; a later small append succeeds after headroom exists. ErrFull already maps to `ErrRetentionFull` → HTTP 507 through the Broker.
- `TestRetainMaintenanceRotatesAtHighWater`: at >80% of the ledger cap the tick rotates the prefix to a half-cap suffix, prunes and compacts under the destructive policy, persists `retain_last_ok` (report.Marker); a second high-water pass at an unchanged marker does not rewrite meta (WAL-churn bound).
- `TestRetainMaintenanceIdleWritesNothing`: a quiet tick runs no pass and writes no marker.
- `TestDeleteFailureRemainsPendingAndDenied` (extended): failed provider delete leaves durable `pending-delete`, durable `attempts`/`next_at`; an immediate retry is skipped, a retry after the first window fails again and pushes `next_at` past 45s — exponential, not the old flat 30s; retry after the window succeeds and resolves the row. The jitter is downward-only (up to -12.5%) so a retry never waits past its exponential bound — a nondeterministic >30s window was a real flake caught by this test.
- `TestSweepObjectsCommandDrainsRetiredObjects` (app) + `TestStoreSweepCommand` (cmd): `store-sweep --config` drains retired objects offline with the service stopped and reports `{"swept": N}`.
- `TestStoreRetainRecordsCommand` (cmd): `store-retain --records --keep-bytes` exports the prefix and prints the JSON report; with `--destroy` it prunes and reports `pruned:true`.
- `TestServiceTickRunsRetainMaintenance` (app): a live `Service.Run` tick rotates records history with no operator — the auto path is real, not only a unit seam.
- `TestBuildWiresRecordsAutoRetain` + `TestReadConfigRejectsUnboundAutoRetain` (app): config `records_archive_dir`/`records_auto_retain` (+ `audit_auto_retain`) wire the destructive policies onto the open journal through `Build`; config validation rejects auto-retain without an archive dir.

Config surface: `audit_auto_retain` / `records_auto_retain` are the explicit destructive policies (opt-in); `records_archive_dir` defaults to `audit_archive_dir`. Without the flags a high-water tick exports only — admission stops visibly at the cap; nothing is silently deleted. Dev stack (`spaces/local/broker/config.json` + live volume config) runs with both flags enabled.

Docs: `docs/operations.md` (records rotation, auto policies, visible ledger stop, backoff, store-sweep) and `docs/services/credential-broker/operations/recovery.md` (rotation + auto-maintenance section) updated.

Not verified yet: live dev-stack rotation on the real volume (in progress), `just check` root, coverage numbers, race.

## Live dev rotation, 2026-11-02

On the real dev volume (broker stopped, dev image `hermes-hub-dev-local-credential-broker:local` with the new binary):

- `store-retain --records --keep-bytes 8388608` exported **342,416 records / 118,713,643 bytes** into `records-1-342416.seal` (199,801,844 B encrypted) + sealed manifest; `pruned:false` — export-only honored.
- `store-retain --records --keep-bytes 8388608 --destroy` verified the existing archive, pruned the prefix across both legs in one transaction and VACUUMed: `primary.db` **327,073,792 → 10,498,048 bytes**; `recovery.db` likewise 10,498,048. `HotBytes` in the report: 8,388,399.
- Broker restart on the rotated store: floor-aware replay opened cleanly, `Credential Broker listening`, 0 restarts, TLS handshake answers on :8787.
- Dev config (`spaces/local/broker/config.json` + live volume config) now sets `audit_hot_max_bytes`/`audit_archive_dir`/`audit_auto_retain`/`records_archive_dir`/`records_auto_retain` — the tick self-maintains audit and records history at 80% high water from here.

Commit `62d04a5`. Module suite green in the SQLCipher image (`go test ./...` all packages, real codec). Remaining honest gaps: archive-dir byte budget (S199.1 partial), root `just check`, race run, coverage delta.

## Archive budgets (S199.1 last gap closed), 2026-11-01

The archive directories themselves are now bounded: `sweepArchiveDir` runs on the same maintenance tick, groups `records-*`/`audit-*` `.seal`+`.manifest.json` pairs, verifies each manifest as this store's archive (fail-closed — foreign or tampered manifests are never deleted), and permanently deletes the oldest verified pairs over the byte (`*_archive_max_bytes`) or age (`*_archive_max_age_days`) limit. The newest verified pair is never deleted — it carries the provenance link the retained floor chains onto.

- `TestArchiveBudgetSweepsOldest`: two rotations → two archives; byte budget < total → oldest pair gone, newest verifies standalone.
- `TestArchiveAgeDeletesOldPair`: 48h-aged pair swept under `max_age_days=1`, fresh pair kept.
- `TestArchiveBudgetSweepsAudit`: same budget semantics for `audit-*` pairs through `AuditRetention`.
- `TestArchiveBudgetKeepsNewestAlone`: a lone archive over budget survives — provenance link never cut.
- `TestBuildWiresRecordsArchiveBudget` (app): `records_archive_max_bytes` reaches the journal through `Build`; `TestReadConfigRejectsUnboundAutoRetain` extended: a byte budget without an archive dir is rejected.
- Full module `go test ./...` green in the SQLCipher image after the sweep.

Dev config sets `audit_archive_max_bytes=512MiB`/`audit_archive_max_age_days=30` and `records_archive_max_bytes=2GiB`/`records_archive_max_age_days=30` — the whole broker storage footprint (hot audit, records chain, object files, archives) is now bounded on dev.
