# CHG-0218 gate-result: SQLCipher engine proof

Status: PASS (engine + atomicity evidence); live dev-stack acceptance outstanding.

## Engine pins

- Engine: SQLCipher 4.6.1 community, built from source
  `github.com/sqlcipher/sqlcipher` tag `v4.6.1`, configure flags
  `--enable-tempstore=yes CFLAGS="-DSQLITE_HAS_CODEC -DSQLCIPHER_CRYPTO_OPENSSL -O2" LDFLAGS="-lcrypto"`.
- Binding: `github.com/mattn/go-sqlite3 v1.14.52`, build tag `libsqlite3`;
  `-lsqlite3` resolves through a shim (`/opt/sqlcipher-shim/libsqlite3.{so,a}`
  -> `/usr/local/lib/libsqlcipher.*`).
- Runtime check: `PRAGMA cipher_version` reports `4.6.1`; every store open
  refuses to run when the cipher pragma is unavailable (`errNoCipher`).
- Reported SQLite inside engine: `3.46.1 ... (SQLCipher 4.6.1 community)`
  (verified via `sqlite3 --version` semantics of the linked library in the
  toolchain image).

## Build evidence

- `services/credential-broker/deploy/Dockerfile` builds the pinned engine once
  in the `build` stage and links a **fully static** broker binary:
  `CGO_LDFLAGS="-L/opt/sqlcipher-shim -static -lsqlcipher -lcrypto -lssl -lpthread -lm -ldl"`.
- `docker build -f services/credential-broker/deploy/Dockerfile` — PASS; final
  image is `FROM scratch` (no glibc/libsqlcipher runtime needed). Only benign
  static-link warnings (getaddrinfo/dlopen) appear; neither symbol is reached
  in broker paths.
- Root Go binaries unchanged: only `credential-broker` carries the CGO dep;
  `cmd/hubctl dev-rebuild` compiles the broker inside the toolchain image
  (`--target build`) and still swaps a static binary onto the dev-core base.

## Atomicity/identity evidence (linux && cgo test suite, real engine)

All in `internal/journal/sqlcipher_test.go`, run inside the toolchain image
against real encrypted files:

- `TestSQLCipherBuildIdentity` — `cipher_version` present and non-empty.
- `TestSQLCipherMarkerRequired` — unactivated `primary.db`/`recovery.db`/
  `ledger.db` refuse service; partial activation (one leg) refuses.
- `TestSQLCipherImportRefusesLiveWriter` — import while a writer holds the
  ledger lock fails closed. Confirmed again in-container: `store-migrate`
  printed `journal unavailable` while `serve` was still running.
- `TestSQLCipherImportAndActivate` — import -> manifest seal -> activate ->
  open -> append -> reopen on encrypted `primary.db`/`recovery.db`.
- `TestSQLCipherUncommittedTailDropped` — rows committed after the last
  acknowledged witness head are dropped on open (one ATTACH transaction on
  `r`+`p`, witness written only after commit).
- `TestSQLCipherWrongKeyRefused` — wrong key fails closed.
- `TestSQLCipherNoPlaintextRecords` — byte-scan of the DB files finds no
  `SQLite format 3\x00` header and no plaintext record material.
- `TestSQLCipherSingleOpen`, `TestSQLCipherRecoveryCatchUp`,
  `TestSQLCipherDivergenceRefused`, `TestSQLCipherWriterFenced`,
  `TestSQLCipherAppendBound`, `TestSQLCipherActivateWithoutImport` — PASS.
- CLI: `TestStoreMigrateCommand`, `TestStoreMigrateSingleCommand` —
  `store-migrate --config` / `store-activate --config` green for both layouts.

## Volume/crash model

- Multi-database commit uses one transaction across `ATTACH`ed `r`
  (recovery.db) and `p` (primary.db); `journal_mode=DELETE`,
  `synchronous=EXTRA` on both; `MaxOpenConns(1)` on a `:memory:` session so
  keys reach the codec at attach time (mattn `Open()` runs
  `PRAGMA synchronous` before application code, which would touch page 1 of
  an encrypted file).
- Witness head is the same sealed anchor as the framed design; it is written
  only after the DB commit lands. Open replays both schemas, binds the head
  digest to the store identity, truncates unacknowledged tails and catches
  the primary up from recovery.
- Kill-matrix beyond process-exit fencing is covered at the row level by the
  tail-drop/catch-up tests; a whole-VM power-loss matrix has not been run —
  recorded as unavailable, not assumed.

## Container end-to-end (synthetic volume)

- `init` -> `serve` (legacy frames) -> `store-migrate` -> `store-activate`
  -> `serve` all inside `hermes-credential-broker:test`.
- Volume after activation: `ledger.db` (encrypted header), `sqlcipher.activated`,
  `substrate-backup/ledger.bin`; service listens on the encrypted store.

## Limitations

- No proof against a physically removed volume mid-commit (kill -9 + fs
  discard matrix not executed); crash durability rests on one-commit +
  witness ordering and is exercised by tail-drop tests only.
- Normalized state/audit/object tables (SPEC-0087 R3 slices S195.*) remain
  in CHG-0219; this gate proves the encrypted records substrate only.
