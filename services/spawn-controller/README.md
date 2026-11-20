# spawn-controller

## Responsibility

Owns space compute in process mode: ensures one harness-runtime child per
(space, agent) and forwards space traffic to it.

## Owns

- `Runtime` - one running space workload: id, space, agent, state, address.

## Does not own

- Message delivery and channels - owned by channel-hub.
- The agent harness itself - owned by harness-runtime.
- Admission, passports and persistent user data - owned by their services.

## API

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /healthz` | launcher readiness probe | read | - |
| `GET /v1/runtimes` | list managed runtimes | read | - |
| `POST /v1/messages` | ensure runtime for (space, agent), forward the message, relay the reply | write-local | `space` + `agent` |

`/v1/*` requires `Authorization: Bearer $HUB_SERVICE_TOKEN` (constant-time
compare): an `Origin` header is refused 403, missing or wrong bearer 401, a
non-JSON POST 415. `/healthz` stays open for the launcher probe.

## State machines

The HTTP path still runs the in-memory skeleton machine:

| From | Event | To | Deadline / sweeper |
|---|---|---|---|
| - | ensure | starting | 10s healthz deadline, spawn fails |
| starting | healthz ok | running | runtime serves |
| running | child exit | exited | removed from registry |
| running | stop / shutdown | - | SIGTERM or CTRL_BREAK, 10s WaitDelay |

The durable v1 machines exist in `internal/spawn` on `shared/fsm` —
`PassportRevisionMachine` (draft, validated, active, superseded, rejected),
`RuntimeMachine` (starting, warm, busy, draining, failed, quarantined,
missing, absent) and `WorkerMachine` (the runtime table without warm).
They match the target tables in `docs/state-machines.md` and are not yet
wired to HTTP (S4).

Deadline per non-final state — revision: `draft` expires to `rejected`
(10m), `validated` expires to `rejected` (1h), `active` and `superseded`
re-arm only (30d `retain` self-loop — the sweeper never tombstones the
live policy or a rollback target). Runtime: `starting` fails (30s),
`warm` drains (15m idle TTL), `busy` drains (2h hold bound — S4: lease
renewal re-arms it; draining removes lease rows in the same transaction),
`draining` quarantines (30s stop budget), `failed`/`quarantined`/`missing`
are `removed` after 24h retention. `absent` is final per generation: a
claim creates a new-generation row. "any" edges fan out to every non-final
state except the edge's own target.

## Tables

`internal/spawn/migrations` creates `spawn-controller.db` on open:
`spaces`, `passport_revisions` + `_history`, `runtimes` + `_history`
(with `generation`), `leases`, `workers` + `_history`, `image_pins`; every
table carries `org` and `owner`. The store is durable and tenant-scoped,
but the HTTP path does not read or write it yet (S4).

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TRUSTED` | - | must be `true`; the service refuses otherwise |
| `HUB_LISTEN` | - | `host:port` the controller binds |
| `HUB_DATA_DIR` | - | own data dir; runtimes spawn under `runtimes/` |
| `HUB_SERVICE_TOKEN` | - | bearer token required on `/v1/*` and forwarded to children; startup refuses without it |
| `HUB_MAX_RUNTIMES` | `32` | upper bound on live children; must be a positive integer |
| `HUB_DATABASE_URL` | `sqlite://<data>/<svc>.db` | reserved store connection string |
| `HUB_MANAGED_MODEL_URL` | - | passed through to spawned runtimes |
| `HUB_MANAGED_MODEL_ID` | - | passed through to spawned runtimes |
| `HUB_FEATURES` | - | passed through to spawned runtimes |

Only the three pass-through names plus `TZ` reach spawned children from the
operator environment; the controller itself sets `HUB_TRUSTED`,
`HUB_SERVICE`, `HUB_LISTEN`, `HUB_DATA_DIR`, `HUB_DATABASE_URL` and
`HUB_SERVICE_TOKEN` on each child. Runtime binaries are resolved next to the
controller's own binary.

## Extension point

`Backend` (`Start(spec)`, `Stop(id)`, `Inspect(id)`, `List()`); first
provider is `process` (trusted single-user dev). Docker, Kubernetes and
firecracker backends arrive with M40.3/M40.15.

## When a dependency is disabled

| Dependency | Behaviour when disabled |
|---|---|
| harness-runtime | message requests fail `unavailable`; runtimes never start |

## Tools it executes

None.
