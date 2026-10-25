---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0055 prepared SQL connectors (issues #41, #121)

Unit and integration coverage: `tcp_forwards` validation rejects duplicate
listen ports, duplicate target env names, low ports and malformed names;
non-fallback controllers reject policies declaring forwards; companion relay
tests CONNECT through an httptest proxy to a live echo target including
post-handshake buffered bytes; endpoint egress merges `*_ENDPOINT`/
`*_ENDPOINT_<n>` host:port values and refuses secret-looking names, IPv6,
userinfo, single-label and malformed targets; catalog/contract validation and
the generated Rust/Node recipes pass for both new entries.

Live evidence on a disposable Docker postgres 16 (read-only role `ro`, no
production data): dbhub at the pinned commit builds with
`pnpm install --frozen-lockfile` + `build:backend`; on stdio it answers
`initialize`, lists `execute_sql`+`search_objects`, returns `SELECT 1` as
`{"one":1,"u":"ro"}` and reports `permission denied for table items` on
INSERT. `DSN=sqlite:///:memory:` (the reviewed preflight value) starts
unconfigured probes; `sqlite::memory:` is rejected by this revision.
txttsql-mcp at the pushed commit answers `initialize`/`tools/list` with no
configuration at all, configures sources from `TXTTSQL_SOURCES`, and its
guard answers INSERT with `only SELECT queries are allowed`. A real
squid 6.13 with the production-shaped ACL returned `200 Connection
established` for the bound endpoint and `403` for a foreign host; the
companion forwarder carried a PostgreSQL StartupMessage (auth request `R`)
and full dbhub + txttsql SELECTs through loopback -> CONNECT -> postgres
(`forward_live_test.go`, skipped unless `HERMES_LIVE_FORWARD` is set).

Not proven: end-to-end admission on a real stack (broker delivery, squid
profile render, container wiring in one shot) — each stage is proven
separately. The stock ToolHive path correctly refuses these definitions.
txttsql-mcp's upstream suite shows one pre-existing Windows failure
(`broker_executes_directly`, non-UTF8 `whoami` output, reproduces on a clean
checkout); dbhub writes a startup banner on stdout that strict stdio clients
must tolerate.
