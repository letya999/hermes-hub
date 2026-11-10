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

## Open operational concern

The dev `/state` bind is backed by Windows even though its writer runs in a
Linux container. The running database must be checked from Linux; host-side
SQLite access while ToolHub holds WAL is not valid evidence. A Docker-owned
volume and a container-side operator CLI are the longer-term storage boundary
if concurrent host administration of the live registry is required.
