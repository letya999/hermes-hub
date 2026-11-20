---
description: Shared infrastructure, trusted process launcher and service test fixtures delivered by M40.2.
last_verified: 2026-11-20
---
# Shared service skeleton

The root Go module contains shared infrastructure and versioned contracts.
Service extraction remains M40.3 onward; the skeleton does not implement new
service-domain behavior. Go internal visibility protects future service internals;
`devcheck boundaries` refuses imports from shared into services or root internal.

| Package | Contract |
|---|---|
| shared/store | Open SQLite/Postgres by URL; numbered embedded migrations, transactional migration lock; Tx with scoped statements and tenant-bound inserts |
| shared/fsm | Validated transition tables, compare-and-swap state, history in the caller's transaction, deadline sweeper |
| shared/httpapi | Former wire transport, typed errors, bounded JSON, bearer authentication and HTTP client |
| shared/auth | Existing signed run/service token verification; issuing stays in identity-hub; claim format is preserved until M40.14 |
| shared/locale | Four existing language catalogs and language negotiation |
| shared/log | JSON slog records with service identity; callers allowlist safe attributes |
| shared/testkit | In-process/HTTP conformance runner and local OpenAI-compatible SSE model fixture |

## Tenant stores

`store.Open(ctx, "sqlite://data/task-engine.db", migrations)` opens a service-owned
SQLite database; `postgres://...` selects pgx. Never log a connection URL.
Migrations are an fs.FS (normally go:embed), named 0001_name.sql, 0002_name.sql,
without gaps. SQLite uses an immediate transaction; Postgres an advisory
transaction lock. Newer schemas and malformed migration sequences refuse startup.

Inside Tx, use `tx.Scoped(store.Tenant{Org: org, Owner: owner})`. Queries are a
restricted single-table SQL subset with `{tenant}` as the first WHERE predicate;
use bound `?` arguments. A complete tenant is mandatory. The marker is replaced
by org/owner predicates and bound values. Comments, multiple statements, joins,
subqueries, negation of the tenant guard and set operations are refused. This is
an infrastructure misuse guard for authored SQL, not an endpoint for user SQL.
Use Scoped.Insert for writes; org/owner always come from the handle. The narrow
QueryAllTenants read path exists for sweeper key enumeration; subsequent writes
are scoped again. Return transaction errors rather than committing partial work.

`OpenVersioned` retains the v0 stores' schema/application-ID contract until each
owner is extracted; this does not migrate every service onto the new schema.

## Trusted process launcher

`hubctl run --mode process --profile minimal --trusted --bin ./bin --data ./data`
starts the selected binaries directly. Family is also accepted. Enterprise has a
service descriptor but process launching it is intentionally refused. Trust defaults
to false; `--trusted=false` overrides `HUB_TRUSTED=true` and refuses before any
binary lookup or data creation.

The Go descriptor in cmd/hubctl/services.go declares binary, localhost port,
relative database path, zone, edges, replica class and profile membership. Only
declared environment names, OS bootstrap variables and computed service wiring
reach children. Databases use `<data>/<svc>.db`. Every binary must exist before
startup changes disk. The launcher waits for `/healthz`; cancellation, an early
exit or readiness failure stops and joins all children. Process mode provides no
network/tenant isolation and is restricted to explicitly trusted installations.

The real spawn-controller and harness-runtime binaries are not extracted yet.
The default minimal/family commands therefore refuse with a named missing binary.
`TestMinimalProfileAnswersMessage` is skipped with this exact gap. External helper
executables test launcher lifecycle only; they do not establish message acceptance.
Browser runtimes and disposable workers are per-space launches, not hub processes.

## Test base and source distribution

`just test <packages>` runs TestTenantInvariants first across repository source directories
(cmd, contracts, internal, shared, services), excludes spaces, and blocks the suite on failure. Each table case uses a fresh SQLite
store and also a unique Postgres schema when HUB_TEST_POSTGRES_URL is set. CI
requires tenant-invariants before checks/postgres-store; checks-verify requires
both jobs. CI execution remains unverified until publication is authorized.

`contracts/<svc>/v1/fake` contains a foreign-service fixture that passes the same
conformance suite as the real owner. task/v1 demonstrates in-process and HTTP
transports using shared/testkit.Transports. The fake model server emits streamed
OpenAI chat completion chunks, a stop event and [DONE] on localhost only.

`internal/devcheck/testdata/tool-catalog.snapshot` freezes names, schema SHA-256,
effects and required rights of hub-agent-tools. TestToolCatalogSnapshotUnchanged
compares it to the live definition and an available integration Git baseline.
The snapshot writer refuses drift at the same catalog version. A source archive
has no Git baseline, but still checks exact definition equality. Catalog changes
still require the full contract process; this skeleton changes no catalog entry.

Source archives include shared and services/README.template.md. cmd/hub, its
zone multiplexer, the dependency baseline/gate and contracts/toolsurface are
removed; ToolHub retains its catalog DTOs locally. Retired service_enable refuses
with or without policy; it cannot write self-service state or restart a runtime.
