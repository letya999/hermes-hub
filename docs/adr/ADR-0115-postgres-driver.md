---
description: PostgreSQL database/sql driver for shared/store.
last_verified: 2026-11-20
---
# ADR-0115: PostgreSQL driver

Status: proposed

## Context

M40.2 needs the same service store and migration suite on SQLite and Postgres.
The Go standard library provides database/sql but no Postgres driver.

## Decision

Use github.com/jackc/pgx/v5/stdlib as the database/sql driver in shared/store.
It supports context cancellation and PostgreSQL transaction advisory locks;
no connection pool framework is added. SQLite keeps modernc.org/sqlite.

## Validation

shared/store and shared/fsm run against an isolated schema when
HUB_TEST_POSTGRES_URL is set. The postgres-store CI job requires this leg.
Local tests use a disposable database, never a user's service database.
