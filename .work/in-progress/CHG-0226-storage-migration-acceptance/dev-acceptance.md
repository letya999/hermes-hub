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

## Live agent run through the whole migrated substrate (2026-11-01)

End-to-end job on the live `spaces/local` stack with zero external sends:
`POST /v1/mock/message` (control plane, `artem_main`, `chat_id=900001`,
`client_id=acceptance-2026-11-01-1`) — the mock channel is the real
pipeline: binding, queue, supervisor spawn, runtime execution, outbox
delivery, all durable writes landing in the migrated stores.

- Job `mock-6638a010…` moved `pending` → `running` → `done` as **rows in
  `spool.db`**; `done/`+`outbox/done/` disk dirs stayed empty.
- Supervisor spawned `hermes-context-6edca983…` + `-ctl` on the dev
  runtime image; a real model turn ran (`usage.jsonl` row:
  `session_id=hub-b481f886…`, `run_id=run_73243c12…`, 15 018 ms,
  7 851 input / 5 output tokens).
- `run-events` rows journal the full honest chain:
  `admitted`(pos 1) → `uncertain`/`run.unknown`(pos 2 — transient state
  recorded, not hidden) → `run.completed`(pos 3, `runtime_generation=
  gen-481`). Same `job_id`/`run_id`/`binding` everywhere — fully
  reconstructable.
- Delivery captured locally per SPEC-0053: `outbox/done/job-…-response.json`
  row (`channel:"mock"`, `stream_position:3`, `attempts:0→1`) plus the
  `mock-sent/` file-sidecar; agent text `storage-acceptance-ok`.
- Cross-service durables: broker `/v1/leases` + `POST /v1/runtime` calls
  at 01:12:30-31 appended **4096 bytes to both encrypted legs in lockstep**
  (`primary.db`/`recovery.db` 174 100 480 → 174 104 576). Gateway log
  `job.outcome` entries carry `job_id`+`run_id`+`principal`+`status`
  (`uncertain` then `completed`) — the trail explains itself.
- `spool.db-wal` grew to 362 592 bytes during the run and checkpointed;
  WAL sidecars stay live under the service.

## Gates re-run after the fix commit (2026-11-01)

- `just docker-check` — full build of all six images (control, core,
  browser, runtime, cli-tools, cell-proxy) then every gate green:
  image-budgets, docker-smoke, pinned Hermes 0.21.0 contract, native
  readmit/approval/layered-health/orphan-inventory/crashed-turn/
  desired-state fixtures, host supervisor smoke, real gateway
  five-minute lifecycle, capability contract, managed-network/
  managed-supervisor/scratch-workload canaries, toolhub api quarantine,
  cleanup.
- `docker build -f services/credential-broker/deploy/Dockerfile` — the
  SQLCipher toolchain image builds clean; static binary links.
- Broker module `python3 scripts/quality.py check` inside the toolchain
  container with `GOFLAGS=-tags=libsqlite3` +
  `CGO_CFLAGS=-I/usr/local/include/sqlcipher` — fmt/vet/openapi/archtest/
  secret-hygiene/docs green, race suite green, **coverage gate 88.97 %
  total, every package ≥85 %** (`internal/journal` 85.33 %), build green.
- `quality.py fuzz` — three 3 s smoke campaigns green; `govulncheck`
  clean; `gosec`/`staticcheck` clean after two `#nosec G202` annotations
  on fixed-schema strings and a `pprev/psize` dead-assign fix.

### Coverage-gate discovery and repair

The first post-acceptance `quality.py check` exposed a real problem,
not a test bug: under a codec-less build (`linux&&cgo` without
`libsqlite3`) the SQLCipher engine tests skip, leaving
`internal/journal` at 51 % — the 85 % gate would fail in CI which runs
`just credential-broker-check` on a plain runner. Fixes applied:

- ci.yml `credential-broker` job now installs `libsqlcipher-dev`, links
  `libsqlite3.so → libsqlcipher.so` and runs the check with
  `GOFLAGS=-tags=libsqlite3` + `CGO_CFLAGS=-I/usr/include/sqlcipher` —
  the same tag the production Dockerfile uses; actionlint clean.
- 14 new refusal-path engine tests in `sqlcipher_test.go` covering the
  gates that were previously only reviewed: existing-artifact/activated/
  missing-anchor/foreign-marker/stale-head/diverged-legs import refusals,
  empty-store anchor init, live-writer fencing on single import, blocked
  marker/backup paths at activation, split-marker and orphan-db dispatch
  refusals, tampered manifest, corrupt-row replay validation (chain
  contiguity, prev link, kind/body presence, size bound, JSON), forged
  witness head (ahead + wrong digest), in-prefix leg divergence, corrupt
  activated db, held recovery lock, conflicting-row and dead-session and
  lost-anchor append poisons, unsafe literal quoting.
- Result: `internal/journal` 80.5 → 85.3 % under the real engine, honest
  coverage of security-critical refusal branches.

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

