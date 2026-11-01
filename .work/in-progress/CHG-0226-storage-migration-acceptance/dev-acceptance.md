# CHG-0226 dev-acceptance evidence

Date: 2026-11-01. Scope: all three durable-store migrations live on the
`spaces/local` dev stack — credential-broker (SQLCipher), toolhub (SQLite),
communication-hub (SQLite spool.db).

## Credential Broker — executed

1. `go run ./cmd/hubctl dev-rebuild --dir spaces/local --env dev credential-broker`
   — built the broker inside the pinned SQLCipher toolchain image
   (`--target build` of `services/credential-broker/deploy/Dockerfile`,
   sqlcipher 4.6.1 + mattn go-sqlite3 v1.14.52 + libsqlite3 shim, fully
   static link) and swapped the binary onto `hermes-hub:0.5.0-dev-core` as
   `hermes-hub-dev-local-credential-broker:local`. Service recreated and
   reported Healthy.
2. `go run ./cmd/hubctl store-migrate --dir spaces/local --env dev --broker --apply`
   — compose `stop credential-broker`, then `docker run --entrypoint
   credential-broker --user 10001` against the three external volumes
   (`hermes-credential-broker-dev`, `-recovery-dev`, `-recovery-key-dev`)
   running `store-migrate --config /var/lib/credential-broker/config.json`
   inside the live service image. Output: `Store migration staged.`
3. `go run ./cmd/hubctl store-activate --dir spaces/local --env dev --broker --apply`
   — `Store activated.` then compose `start`. Container back Up, log shows
   `Credential Broker listening.` followed by real traffic
   (`POST /v1/leases`, `POST /v1/runtime` — 24–49 ms).

## Credential Broker — verified post-activation

- `/data/ledger/primary.db` and `/rec/recovery/journal/recovery.db` both
  174 088 192 bytes, encrypted headers (no `SQLite format 3\x00`), equal
  size — replicated legs in step.
- `sqlcipher.activated` + `sqlcipher.manifest` present on both legs.
- Legacy `ledger.bin` retired into `substrate-backup/` on both legs
  (124 824 459 bytes, untouched source).
- DB mtimes post-date service start — post-activation appends land in the
  encrypted databases (the `/v1/leases`+`/v1/runtime` calls above are the
  appends; witness head written after commit per substrate design).
- Live-writer fencing observed incidentally: `store-migrate` refused with
  `journal unavailable` while the service was still running.

## ToolHub — executed and verified live

1. `docker compose ... stop toolhub` (live JSON writer fenced by
   `store.json.lock`).
2. `hubctl store-migrate --toolhub-store spaces/local/runtime/toolhub/store.json
   --apply` → `store.db` + `store.db.manifest.json`; 110 collections,
   `source_sha256 == db_digest`.
3. `hubctl store-activate ... --to store.db --apply` → `store.json` retired
   to `store.json.legacy` + `store.json.substrate-backup`.
4. Host-created files cannot be chmod'd on the Docker Desktop Windows bind;
   `store.db` was recreated inside the container (`cp`+`mv`) so it became a
   container-owned `0600` file — required by `sqlitedb.Open`'s hardening
   `os.Chmod`.
5. `dev-rebuild toolhub` — compose re-render flipped
   `HUB_TOOLHUB_STORE=/state/toolhub/store.db` automatically (store.db
   present, store.json retired); recreated on the dev overlay and Healthy.
6. Verified live: env var inside the container points at `store.db`; WAL
   sidecars (`store.db-shm`/`-wal`) active; a read-only copy of the live db
   opens under modernc.org/sqlite with 60 `ent` + 12 `kvmap` + 38 `listrec`
   rows — exactly the 110 migrated collections; service processed projection
   revisions in the log after start.

## Communication Hub — executed and verified live

1. `dev-rebuild communication-hub` (fresh gateway + dbFS code), then
   `docker stop` on the service.
2. Sidecar `docker run --rm -v hermes-hub-local-dev_communication-hub-data:/data
   -v <fresh-hubctl>:/tools:ro --entrypoint /tools/hubctl <dev-overlay-image>
   store-migrate --spool /data --apply` → `spool.db` + manifest, 1 record
   (event-layout manifest; the dev spool is otherwise empty).
3. Same sidecar `store-activate --spool /data --apply` → `records.activated`
   written, `event-layout.json` retired to `substrate-backup/`.
4. First restart crash-looped: `open /data/events: not a directory` —
   `migrateEventLayout` still read the retired manifest via raw `os`, hit
   the `events` fence *file* with `os.ReadDir` (ENOTDIR on Linux; the bug
   was invisible on Windows where ReadDir-on-file yields ENOENT). Fixed by
   reading the manifest through the record backend
   (`s.readEventRecordLocked`) plus closing the backend handle on failed
   opens; regression tests `TestSpoolDBActivatedReopenReadsLayoutRow` and
   `TestSpoolDBActivatedRejectsBadLayoutRow` (fail-closed on a corrupt
   manifest row) added and green.
5. Verified live after the fix: gateway Healthy on `records.activated` +
   `spool.db`; authenticated control-plane probe
   `POST /v1/tasks {"name":"dev-acceptance-probe"}` → `200 OK` (`t-19db03a3`)
   grew `spool.db-wal` 0 → 4152 bytes (row write committed through the
   service); `DELETE /v1/tasks/t-19db03a3` → `200 OK` second write; the
   `tasks/` disk dir stayed empty — records are rows now.

## Known limitations / not yet evidenced

- Broker first-open replay of ~200k records takes ~30 s (one-time per
  restart while the journal replays into memory; acceptable for dev, watch
  prod latency).
- No power-loss/volume-loss matrix on the live stack (unit-level tail-drop
  and catch-up tests only) — recorded as unavailable in CHG-0218 gate.
- The dev spool contains almost no organic traffic; the toolhub bind-mount
  chmod workaround and the communication near-empty import are dev-stack
  realities, not prod procedures (prod paths run migration in the service
  image on Linux volumes where chmod works).
- Normalized broker state/audit tables (SPEC-0087 R3, CHG-0219 S195.*) are
  not part of this acceptance; the encrypted records substrate is.
- prod untouched; `dev` branch not updated by this evidence.

