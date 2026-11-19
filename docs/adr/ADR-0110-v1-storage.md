---
description: v1 storage - one database per service, SQLite by default and Postgres by the same connection string, portable SQL, embedded numbered migrations under a lock, every row tenant-scoped, no service state in .json or .jsonl files.
last_verified: 2026-11-20
---
# ADR-0110: v1 storage

Status: proposed (M40.1 #599, CHG-0334). Direction agreed with the owner on
2026-11-20; record text awaits owner review.
Delivery: M40.2 (`shared/store`, #600); every service issue M40.3..M40.13
moves its state; M40.15 (Postgres scale-out, #613).
Amends: ADR-0091 (SQLite substrate) - SQLite stays the default, now one
database per service instead of per-space databases inside ToolHub and
Communication. Replaces the v0 file journals of ADR-0088 for service state.

## Context

v0 keeps service state in dozens of files: `supervisor.json`,
`transitions.jsonl`, `signals.jsonl`, `apply-journal.json`,
`governance.json`, `cursors.json`, `executor-leases.json`, `budget.json`,
`model-grants.json`, per-user routine and assignment JSON, `writes.jsonl`,
plus SQLite stores (`store.db`, `spool.db`) and file-location state (spool
`pending/`, `running/`, `done/`). Several wedges in the 2026-11-19 audits
came from two writes that must agree landing in different files.

## Decisions

1. **One database per service.** A service owns exactly one database and
   no other service opens it (ADR-0105 D7).
2. **SQLite default, Postgres by connection string.** `sqlite://<path>` or
   `postgres://...` selects the driver; the SQL is portable across both.
   Stateful services run one copy on SQLite or N copies on Postgres, with a
   lease so only one copy runs each background loop.
3. **Migrations.** Embedded, numbered SQL files applied in order under a
   migration lock, so N copies can start at once.
4. **Tenant scope.** Every row carries `org` and `owner`. The query helper
   in `shared/store` refuses SQL without both predicates; it fails closed.
   A cross-tenant read returns `not_found`.
5. **No service state in files.** No `.json` or `.jsonl` service state and
   no state encoded in file location. Files hold only user data (vault,
   workspace, sites), input YAML passports, harness session files inside
   the workspace; logs go to stdout.
6. **Import, then delete.** Each service imports its v0 files on first
   start with a dry-run report and refuses unmapped records; the importer
   is deleted one release after v1 (M40.16).

## Enforcement

`TestStoreRefusesUnscopedQuery`, `TestMigrationLockSerializesConcurrentStart`
and the tenant invariant runner (M40.2); the store suite on Postgres in CI
(M40.2); `TestOnlyOneCopyRunsSweeper` (M40.15); per-service acceptance "no
`.json`/`.jsonl` state written" (SPEC-0100).

## Alternatives

- **Shared database for all services.** Rejected: it recreates the coupling
  through tables.
- **Postgres only.** Rejected: the minimal profile must run as bare
  binaries without a database server.
- **ORM or code generation.** Rejected: embedded SQL is enough (rule Z2).

## Preserved v0 invariants

- Fail closed on a foreign or unreadable store (CHG-0331 owner fence).
- Dev and prod keep separate data.
- Credentials are never stored outside credential-broker (ADR-0112).
