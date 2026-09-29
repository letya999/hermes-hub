---
description: User-scoped runtime diagnostics and log access through ToolHub (issue 125).
last_verified: 2026-09-28
---

# CHG-0053 User-scoped diagnostics

## Change

- New ToolHub control op `diagnostics` projects the bounded operator log file
  back to an authenticated principal. Scope is derived server-side from store
  bindings: the caller's runtime container `hermes-context-<sha256(principal,
  context, gateway)[:8]>` plus `work-<id>` workload families for their own
  active and disabled bindings. Model-supplied owner IDs or container names are
  impossible to pass; foreign selectors return empty results.
- Bounded queries: `tail` 1-500 (default 200), RFC3339 `since`/`until`,
  `search` substring up to 256 bytes, `severity` info|warn|error, `workload`
  selector by workload_id / binding_id / definition_id / "runtime". The scan
  reads at most the last 8 MiB of the collected file, each returned line is
  capped at 32 KiB, the whole payload at 192 KiB, and concurrency at 4.
- Redaction before projection: bearer values, `key=value` secret fields,
  credentialed URLs, JWTs, Telegram bot tokens, common provider prefixes and
  AWS access key IDs. Host-supervisor lines are projected only when they name
  the caller's own runtime container.
- Disable/revoke shrinks the visible set because scope is computed from live
  store state on every call; a removed binding disappears entirely.
- The collector now also tails `work-*` containers so connector workload logs
  land in the file at all (previously only `hermes-*`).

## Contract

- No host paths, Docker IDs, raw Docker/system APIs, other users' data,
  environment values or Broker payloads are exposed; the op returns
  `{enabled, workloads, runtime, lines, truncated}` only.
- Diagnostics disabled (`HUB_DIAGNOSTICS_DIR` unset) returns
  `{enabled: false}` instead of an error; a missing file returns an empty
  result with `detail`.
- Rejected: unknown severities, non-RFC3339 bounds, non-integer tails, and any
  authority argument via the existing `RejectAuthorityArguments` gate.

## Verification

- `internal/toolhub/diagnostics_test.go`: cross-user isolation, secret
  redaction, tail/window/search/severity bounds, selector narrowing, revoke
  removes visibility, disabled/missing handling, cancellation, concurrency,
  authority-argument rejection.
