# CHG-0294: Telegram admission and ToolHub SQLite latency

## Goal

Make the owner's Platform, Files and Terminal Telegram jobs visibly queued from
receipt, and keep ToolHub authorization and MCP calls responsive while its
registry uses SQLite. Preserve fail-closed authorization and the SQLite store.

## Incident evidence

- Three Telegram updates were received at 04:42:19, 04:42:34 and 04:42:45 UTC.
  The first progress message went out at 04:44:41, after a 120-second supervisor
  admission wait. Two jobs failed; the remaining reply did not execute its
  requested file tools.
- Model relay saw two five-second `models.use` PDP timeouts and denied with 403.
  ToolHub requests during the incident took up to 13 seconds.
- Docker inspect identifies the ToolHub `/state` mount as a Docker Desktop bind
  from `C:`. The Linux container's `store.db` passed `PRAGMA integrity_check`
  and loaded in about five seconds. Reading its live WAL concurrently through
  Windows SQLite gave a misleading malformed-image result; do not use that
  path as a live health check.
- SQLite activation initially failed because the unprivileged process tried to
  chmod a private `0600` database on that bind. Once provisioned as uid/gid
  10001 and mode `0600`, the service could open it safely.
- MCP connection to ToolHub initially took 10–15 seconds. Revalidating the
  entire persisted projection on every request and every 500 ms was the
  immediate latency source. A static tool now connects in about 0.1–0.2 seconds
  in the local dev stack.

## Work and acceptance

1. Send one editable localized queued status immediately after durable Telegram
   enqueue, and keep its elapsed timer active before supervisor admission.
2. Permit an already private SQLite file to open without repeating chmod;
   continue to reject non-private files and foreign store changes.
3. Use SQLite `data_version` for unchanged-store checks and fingerprint the
   persisted projection inputs so only changed data triggers full validation.
   Serialize concurrent refreshes. Keep direct calls fail-closed on live rights.
4. Raise the PDP client timeout from five to twenty seconds as a bounded guard
   against transient ToolHub load; it remains fail-closed.
5. Rebuild ToolHub, communication hub, model relay and supervisor in the dev
   stack. Verify Linux SQLite integrity, actual ToolHub calls for `artem_two`,
   container images and no restart loop.
6. Run focused regression tests, vet, `just check` and affected-package tests.
   Do not claim the original Telegram jobs completed without a fresh user run.
7. Keep `Store.Reload` snapshot I/O outside the state lock while coalescing
   concurrent reloads. Reject a snapshot superseded during the read so callers
   cannot authorize against stale rights.

## Reload follow-up evidence (2026-11-10)

- Source HEAD: `d56a0e24a6a6a2fef4bd632125c7dd362aaa03aa`; this local fix is
  uncommitted, so it has no remote CI or image acceptance.
- `TestStoreDBReloadDoesNotBlockReadersDuringLoad` and
  `TestStoreReloadRejectsStaleSnapshot` passed on SQLite and JSON paths.
  `go vet -p=2 ./internal/toolhub`, `just check`, and
  `just test ./internal/toolhub` passed (package: 197.540 s).
- `hubctl dev-rebuild --dir spaces/artem_main --env dev toolhub` recreated the
  running dev ToolHub with `hermes-hub-dev-artem_main-toolhub:local`. The MCP
  endpoint returned 401 in 0.010 s and the service-only automation endpoint
  returned 401 in 0.020 s without credentials, confirming live reachability.
- The authenticated `assignments.list` path from a spawned runtime has not
  been rerun after this rebuild. Its latency and result remain unverified.

## WSL ToolHub recovery evidence (2026-11-11)

- Moved the stopped owner and managed-space dev stacks from a Docker Desktop
  Windows bind to Ubuntu ext4 under `/home/artem/hermes-hub-deploy`; copied
  SQLite WAL/SHM with numeric owners preserved and checked destination
  `PRAGMA integrity_check=ok`. Source spaces were left intact.
- Re-rendered both spaces, then rebuilt only the ToolHub and supervisor dev
  images through `hubctl dev-rebuild`. The Docker socket is GID 1001; the
  supervisor now runs with that supplemental group. The rendered runtime env
  files and generated skill/config views now admit the supervisor UID/group.
- Authenticated MCP `tool_search` found `terminal_exec`, `terminal_job` and
  every requested `document_*` tool in 10–60 ms per exact lookup. A real
  `terminal_exec` in the managed space returned SHA-256
  `51eb9ee0566b3eae2ca5204ddcb76893970f49dc93e2f6b00c4ec94088c07ed5`,
  three lines, exit code 0. A two-second background command appeared in
  `terminal_job list` as running, then `poll` returned `done`, exit code 0,
  and `terminal job complete`.
- The first runtime spawn took about 21 s. Subsequent isolated terminal
  commands took 3–5 s including scratch-container startup; exact ToolHub
  lookup remained under 60 ms. No publication or remote CI was run.
- `assignments.list` still timed out at about 31 s after the filesystem move:
  the admitted ToolHub call held its sole SQLite connection while
  communication-hub called back into ToolHub for `tasks.delegate`. Releasing
  admission before assignment/routine/watch dispatch and rechecking rights
  removed that self-wait. Live `assignment_list` completed in 859 ms and then
  483 ms; a concurrent terminal and background job check also completed.

## Open operational concern

The live dev `/state` bind is now on Ubuntu ext4; the old Windows copy remains
as a stopped fallback. Do not inspect a live ToolHub WAL from Windows. These
uncommitted changes have no remote CI acceptance.
