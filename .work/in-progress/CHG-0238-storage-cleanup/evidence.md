# CHG-0238 evidence — 2026-10-31

Only safe aggregate counts are recorded here; no secret bytes, credential-store
keys, message bodies or audit records are copied into the repository.

## Local storage

- Before cleanup: Communication Hub spool 15,313 files / 71,283,080 bytes.
  It contained 10,923 orphan `.probe-result`, 11 `.digest-result` and 34
  `.script-output` files older than 72 hours; no live occurrence `.json` was
  present. Pending/running jobs and pending/sending deliveries were all zero.
- The new gateway image compacted those sidecars on startup. Afterward the
  digest-bound offline cleanup applied successfully to the stopped exact
  dev spool volume: 4,298 files / 58,299,403 bytes both before and after that
  second apply. The gateway restarted; its selected container image digest
  matched the rebuilt dev image and HTTP port 8081 responded.
- Five unattached `hermes-storage-merge-20261031-130940-*` Broker backup
  volumes, including the old key copy, were removed by exact name. The active
  Broker container and its distinct primary, recovery and key volumes remained.
- Direct deletion of 101 older ignored host `.log`/`.old`/`.bak`/`.tmp`
  files in `spaces/local` was rejected by automatic approval review with the
  sole reason `blocked by policy`. The same guarded PowerShell action was
  rejected again after explicit owner authorization. No alternative deletion
  route was used.
- A separate digest-bound offline cleanup removed one 15-day-old `api.test`
  binary and 72 stale E2E files: 73 files / 53,701,192 bytes. One
  `settings-baseline` recovery marker was retained. The stopped gateway
  restarted and the spool plan then reported 4,225 files / 4,598,160 bytes.
- The oversized local ToolHub audit was recovered by CHG-0239: all 181,794
  records are preserved in a verified compressed archive and the active
  bounded ledger accepts new writes. The original uncompressed duplicate was
  removed after verification.
- Global legacy readers were retained because other dev spaces still contain
  schema-1 data. Removing them now would prevent those spaces from starting
  or reading their state.

## Verification

- Focused hubctl cleanup and Communication Hub compaction tests passed;
  `TestCompactDropsOnlyAgedOrphanOccurrenceSidecars` also passed with `-race`.
- Full `e2e mock --scope core`: 151 cases, 122 PASS, 29 BLOCKED because their
  fixtures require additional evidence, 0 FAIL. The initial baseline had one
  intermittent `E2E-003` failure; isolated race tests and the full final pass
  succeeded. Mock results do not establish live external account behavior.
- `just check` passed, own Go statement coverage 86.27% (required 85%).
- Broker `scripts/quality.py check` passed inside the local Go 1.27 Linux
  image, including race tests, documentation, security hygiene and 90.3%
  statement coverage. The `just` wrapper was unavailable here because WSL
  lacks `gofmt`; native Windows cannot build its Linux-only `securefs`.
- The final `hubctl dev-rebuild` recreated Communication Hub; its running
  image digest matched `hermes-hub-dev-local-communication-hub:local`, HTTP
  responded, active queues remained empty and all three stale sidecar types
  remained at zero. Docker `TestHubStdioReminderContract` passed.
