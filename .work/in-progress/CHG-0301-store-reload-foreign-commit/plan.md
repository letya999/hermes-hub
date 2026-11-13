---
description: Detect foreign-process store.db commits that data_version cannot observe (Docker Desktop bind mounts) so a host-side apply no longer wedges ToolHub authorization until restart — incident telegram-837846093+ (silent chat).
---

# CHG-0301: store reload sees foreign commits on broken mounts

## Incident

2026-11-12 ~20:48–21:33 UTC: `hubctl space apply` committed new capability
profiles (passport pins) to `store.db` from the Windows host while ToolHub
ran in the shared dev container. The container's `PRAGMA data_version`
never advanced for that foreign-process commit, so `Store.Reload()` stayed
a no-op: in-memory profiles kept the old `PassportRevision` while
`generated/passports/current` pointed at the new bundle. Every
`requireActivated`/`requireAssertedActivation` then failed
`checkActivePassport` with `passport activation mismatch` → HTTP 503 for
`/v1/rights/decide`.

Effect: `artem_two` runtime spawn collapsed into `runtime_recovery_backoff`
(separate missing-image cause), Telegram jobs 837846093/097/098 failed, and
even the error replies were suppressed because `channels.send` decided 503 —
total silence. A container restart forced a fresh `Load` and recovered.

Noise resolved by the same root: `routine tick failed stage=0` and
`removal reconciliation … passport activation mismatch` every ~5–30 s were
the same stale pin, not an envelope defect — they stopped post-restart.

## Changes

- `internal/sqlitedb/sqlitedb.go` + `internal/toolhub/store_db.go`:
  `sqlitedb.OpenRollback` opens the ToolHub store in rollback-journal
  (`journal_mode=DELETE`) mode. Root cause fix: a WAL's shared-memory index
  cannot propagate through a Docker Desktop bind mount, so each WAL
  connection's view froze at map time — `data_version` never advanced for
  foreign commits, a second connection failed `SQLITE_IOERR_SHORT_READ`
  (522), and blind writes/checkpoints physically erased foreign frames.
  Proven live: a host-side `space apply` profile row (seq 159) was
  committed, adopted, then destroyed by the container's next save within
  ~90 s. Rollback mode has no shared index: every read sees the
  authoritative file and file locks serialize host/container writers
  (verified cross-process through the bind mount).
- `internal/toolhub/store_io.go`:
  - `changedOnDiskLocked` (SQLite branch): `data_version` first, then a
    rate-limited content hash of `store.db` (of `-wal` when a WAL-era file
    still exists) — `storeFileHash`. Mtime/size stamps are unusable (WAL
    churn ticks them without commits). `changedOnDisk(forced)` lets
    rechecks inside a reload critical path bypass the poll limiter; the
    initial probe stays rate-limited.
  - `loadStoreSnapshot` copies `store.db`+`store.db-wal` to a temp dir and
    opens the copy — a second live connection cannot be trusted on the
    bind mount. Adopted snapshots restore `path`/`savedPath`/`dbFile`/
    `diskStamp`/`fileHash` of the real store.
  - Post-load adoption fence: live file hash vs the snapshot baseline —
    a commit during the fetch refuses with `ErrConflict`, next pass
    converges.
  - `storeSnapshotDigest` reads `meta.digest` from a byte copy — the
    save-fence arbiter.
  - `Store.fileHash` baseline, set on load and after each save.
- `internal/toolhub/store_db.go`:
  - `saveDBLocked`: on file-hash drift, arbitrates via
    `storeSnapshotDigest` — a real foreign commit refuses `api.ErrConflict`
    (mutation ops roll back their in-memory change; next `Reload` adopts);
    the store's own out-of-band writes re-baseline `fileHash` and proceed.
  - The in-transaction persisted-digest check is restored: in rollback
    mode the connection's view is truthful again (it was removed only
    while the frozen WAL view could wedge post-adoption saves forever).
- `internal/toolhub/store_db_test.go`:
  - `TestStoreDBSaveRefusesToOverwriteForeignCommit` — foreign `Save` +
    pinned `data_version` → own `Save` refuses `ErrConflict`, foreign rows
    survive, `Reload` adopts them.
  - `TestStoreDBReloadAdoptsForeignCommitWhenDataVersionStalls` — pinned
    `data_version` + foreign `Save` → reload adopts the new snapshot.
  - `TestStoreDBReloadRefusesSnapshotOvertakenDuringLoad` — pinned
    `data_version` + commit inside `storeLoad` → one `ErrConflict`, next
    reload adopts the latest snapshot.
  - `TestStoreDBReloadUsesDataVersion` — stamp churn no longer triggers
    loads; digest-equal content is adopted silently.
  - `TestProjectionFingerprintTracksPersistedInputs` — drifted stamp is
    non-cacheable until one `Reload` re-baselines; fingerprint unchanged.
- `docs/operations.md`, `docs/services/toolhub.md` — rollback-journal
  rationale, file-hash detection, copy-snapshot loading and the digest
  fences; no more "WAL and SHM" on the mount-copy checklist.

## Live incident repair

The pre-fix WAL writer had already torn the shared store (ledger rows from
one epoch, `meta.digest` from another) — the digest check then wedged boot
into a crash loop. Verified the row set was self-consistent
(`storeFromSnapshot` clean, profile rev/passport pins match `current`),
rewrote `meta.digest` to the recomputed `rows-v1` value, and re-derived
the erased profile row via `DeriveSpaceRights` (seq 159, rev 87 →
`r-3c345fbe`).

## Out of scope

- Onboarding/reconciler envelopes built without `Environment` — investigated
  and found harmless: they only reach the broker client path, never
  `checkActivePassport` (an empty `CapabilityProfile` fails earlier at
  `capabilityProfileLocked` regardless of env).
- `hubctl up` pruning `:local` overlay images that `dev-runtime-image` pins —
  the second half of the outage (spawn `managed_topology`); separate fix.

## Verification

- `go test -short -run 'TestStoreDBReload|TestProjectionFingerprint|TestStoreReload|TestStoreSave' ./internal/toolhub` — green.
- `go test -short ./internal/toolhub ./internal/sqlitedb` — green (114 s).
- `just check` — green (format, docs, vet).
- Delivery: `dev-rebuild toolhub` — store auto-converted WAL→DELETE on
  first open, container healthy.
- Live: host-side `DeriveSpaceRights` committed seq 159 (rev 87 →
  `r-3c345fbe`); `decide artem_two models.use` returned 200 without
  restart; the row survived 150 s+ of container save/reconcile cycles and
  the reconcile conflict noise stopped.
