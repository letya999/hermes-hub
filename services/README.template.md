# <service-name>

Copy this file to `services/<service-name>/README.md` and fill every section.
Keep it to one page. `go run ./cmd/devcheck docs` fails when a `## ` heading
below is missing from a service README. Write "None." under a heading that
does not apply; never delete the heading. Rules: docs/engineering.md.
State machine tables are copied into docs/state-machines.md.

## Responsibility

One sentence: what this service does and for whom.

## Owns

The records and resources only this service writes. One line each.

- `<Record>` - what it is.

## Does not own

What callers might expect here but belongs elsewhere, with the owner.

- `<thing>` - owned by `<service>`.

## API

Contract `contracts/<service-name>/v1`. One row per route.

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /v1/...` | ... | read | - |
| `POST /v1/...` | ... | write-local | `<field>` |

## State machines

One table per machine. Every non-final state names its deadline and who
acts next (runtime, owner, sweeper).

| From | Event | To | Deadline / sweeper |
|---|---|---|---|
| - | ... | ... | ... |

## Tables

Database `<service-name>.db` (SQLite default, Postgres by connection string).
Every row carries `org` and `owner`.

| Table | Purpose | Key |
|---|---|---|
| `<table>` | ... | `org, owner, id` |

## Environment

Every variable the binary reads. An unknown `HUB_*` variable fails startup.

| Variable | Default | Meaning |
|---|---|---|
| `HUB_<SERVICE>_DB` | `sqlite://./data/<service-name>.db` | store connection string |

## Extension point

The provider interface this service exposes and its first provider, or
"None.".

## When a dependency is disabled

One row per service this one calls. An empty `HUB_<SVC>_URL` means disabled.

| Dependency | Behaviour when disabled |
|---|---|
| `<service>` | falls back to ... / feature off with message ... |

## Tools it executes

Agent tools whose executor is this service (docs/tools.md), or "None.".

| Tool | Effect |
|---|---|
| `<tool_name>` | read |
