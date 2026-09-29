---
id: CHG-0055-sql-connectors
issue: 41, 121
---
# Prepared SQL connectors: txttsql-mcp primary, dbhub fallback (issues #41, #121)

## Decision

Two exact-source prepared entries on the generic ToolHub lifecycle:

- `txttsql` — `letya999/txttsql-mcp` (Rust, AGPL-3.0-only), the reviewed primary
  SQL engine: read-only analytics over PostgreSQL/CockroachDB/ClickHouse with
  server-enforced read-only transactions, schema allowlists, row/timeout caps.
- `dbhub` — `bytebase/dbhub` (Node, MIT), the fallback engine: env-driven
  (DSN / DB_* / SSH_* variables), SSH tunnel support via ssh2.

## Connectivity: bounded TCP egress via companion forwards

Isolated workloads run on a per-workload `--internal` network; the only outbound
path is the pinned Squid egress proxy, which already permits `CONNECT host:port`
for reviewed ACL entries. Neither sqlx nor node-pg speaks HTTP CONNECT, so the
companion (`hubctl companion`, already the docker-fallback entrypoint
supervisor) gains `tcp_forwards`: loopback-only listeners that relay each
accepted TCP stream through Squid CONNECT to one bound destination. The
destination comes from a non-secret `*_ENDPOINT` credential env value
(`host:port`), which the existing readiness merge now also appends to the Squid
ACL — the same value feeds both the ACL and the forwarder, so they cannot drift.
The ToolHive-only admission path rejects definitions that declare forwards
(fail closed); the docker fallback implements them.

Managed SSH tunnel = the same mechanism: a `*_SSH_ENDPOINT` forward to a
bastion `host:22`, with dbhub's native `SSH_*` env config pointing at
`127.0.0.1:<forward-port>`. txttsql has no SSH client; its path is bounded TCP
egress, which the issue accepts as the alternative.

## Broker contracts

- `txttsql-sql` (issue #121): `sources` JSON array -> `TXTTSQL_SOURCES` env
  (upstream patch: env sources + resilient no-config start, since preflight
  `tools/list` cannot supply a config file), optional `password_1..3` ->
  `TXTTSQL_PASSWORD_{1,2,3}` env refs used inside the sources JSON, required
  `endpoint` + optional `endpoint_2/3` -> `TXTTSQL_ENDPOINT{,_2,_3}`, optional
  `ca_pem` -> file `/run/txttsql/ca.pem`, optional `config` TOML -> file
  `/app/config.toml` for full-feature deployments (memory/plugins/ontology).
- `dbhub-sql` (issue #41): `dsn` or `db_*` fields -> `DSN`/`DB_*` envs,
  `ssh_*` -> `SSH_*` envs (dbhub accepts a base64 key in `SSH_KEY`),
  `endpoint`/`ssh_endpoint` -> `DBHUB_ENDPOINT`/`DBHUB_SSH_ENDPOINT`,
  optional `ca_pem` -> file `/run/dbhub/ca.pem` referenced from the DSN.

Read-only is enforced by txttsql's read-only transactions + schema allowlists
and, for dbhub, by the read-only database role (documented; its TOML
`readonly` flag is unreachable via env). Mutations stay a separate future
grant — no write entry is shipped.

## New machinery (minimal)

- `ExecutionPolicy.tcp_forwards` `[{listen, target_env}]` on the immutable
  definition + validation + receipt equality.
- Companion `tcp_forwards` yaml/json config: loopback listener -> CONNECT via
  workload `HTTP_PROXY` -> splice. Unset target env = forward inactive.
- `credentialEgressHosts` also emits `host:port` from non-secret env names
  ending `_ENDPOINT` or `_ENDPOINT_<n>`.
- Prepared entry `preflight_environment`: reviewed literal envs for the
  unauthenticated tools/list probe only (dbhub needs `DSN=sqlite::memory:`;
  it exits without a config). Never applied to the submitted-credentials probe.
- Prepared entry `tcp_forwards` -> `ExecutionPolicy`; `target_env` must be an
  env-delivered connection field.

## Non-goals / deferred evidence

No second forwarder sidecar, no ToolHive-path forwards, no per-user ontology or
SQLite memory mounts in v1 (generic workloads reject host state mounts; Broker
state files cap at 64 KiB), no write profile. Live acceptance evidence (real
query, restart/reconnect/revoke/upgrade/two-user isolation) is tracked
separately — this change ships reviewed data + the connectivity mechanism +
local verification.
