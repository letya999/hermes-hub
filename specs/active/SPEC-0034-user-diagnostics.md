# SPEC-0034: User-scoped runtime diagnostics

Frozen: 2026-09-28. Extends SPEC-0031 (automatic bounded diagnostics) with a
user-facing projection. Closes issue #125.

1. The ToolHub `diagnostics` control operation serves runtime health and log
   lines to the authenticated principal only. Visibility is computed from
   live store bindings and the principal's runtime container name; tool
   arguments can narrow but never widen scope. No host path, Docker ID, raw
   Docker/system API, environment value or Broker payload is exposed.
2. A caller sees their own runtime container plus `work-<id>` families of
   workloads owned by their bindings in active or disabled status. Revoked or
   removed bindings stop contributing workloads immediately; stale selectors
   return empty results rather than foreign data.
3. Queries are bounded: `tail` is 1-500 lines (default 200), `search` is a
   case-insensitive substring of at most 256 bytes, `since`/`until` are
   RFC3339 bounds, and `severity` is info|warn|error. The read scans at most
   the last 8 MiB of the collected file, a returned line is capped at 32 KiB,
   the response payload at 192 KiB and concurrency at 4.
4. Lines are redacted before projection: bearer values, `name=value` secret
   fields, credentialed URLs, JWTs, Telegram bot tokens, provider key
   prefixes and AWS access key IDs. Host-supervisor lines are projected only
   when they name the caller's own runtime container, keeping restart and
   reaping diagnostics useful without exposing other users' lifecycles.
5. When `HUB_DIAGNOSTICS_DIR` is unset the operation returns
   `{enabled: false}` rather than an error; a missing file returns an empty
   result.
