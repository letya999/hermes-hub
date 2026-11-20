# spawn-controller

## Responsibility

Owns space compute in process mode: ensures one harness-runtime child per
(space, agent) and forwards space traffic to it.

## Owns

- `Runtime` - one running space workload: id, space, agent, state, address.
- `Space` - the tenant-owned managed-space registry row.
- `PassportRevision` - one submitted layer document set and its lifecycle;
  `pruned` is the retention tombstone past the keep slack.
- `EffectiveSpec` - the immutable compiled bundle the active layers produce,
  keyed by content digest and served to consumers as policy slices.

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
| `POST /v1/spaces` | register the tenant space | write | `idempotency_key` |
| `POST /v1/spaces/{space}/layers/{layer}/revisions` | submit one layer's documents as a draft revision | write | `idempotency_key` |
| `POST /v1/spaces/{space}/revisions/{id}/validate` | compile the draft against the other layers | write | `idempotency_key` |
| `POST /v1/spaces/{space}/revisions/{id}/activate` | cut the revision in one transaction | write | `idempotency_key` |
| `GET /v1/spaces/{space}/revisions?layer=` | list revisions, optionally by layer | read | - |
| `GET /v1/spaces/{space}/effective` | the active layers' compiled spec | read | - |
| `GET /v1/spaces/{space}/policy/{service}?digest=` | immutable policy slice for one consumer | read | - |

`/v1/spaces*` is the spawn/v1 contract (`contracts/spawn/v1`): tenant
(`org`, `owner`) travels in the body on writes and the query on reads; the
policy route answers `Cache-Control: public, max-age=31536000, immutable`
with `ETag` = digest.

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
`PassportRevisionMachine` (draft, validated, active, superseded, rejected,
pruned), `RuntimeMachine` (starting, warm, busy, draining, failed,
quarantined, missing, absent) and `WorkerMachine` (the runtime table
without warm). They match the target tables in `docs/state-machines.md`;
`PassportRevisionMachine` serves the `/v1/spaces` contract, the runtime
and worker machines are not yet wired to HTTP (S4).

Deadline per non-final state — revision: `draft` expires to `rejected`
(10m), `validated` expires to `rejected` (1h), `active` and `superseded`
re-arm only (30d `retain` self-loop — the sweeper never tombstones the
live policy or a rollback target), and activation prunes the oldest
superseded rows past the keep slack (3) to the final `pruned` state.
Runtime: `starting` fails (30s),
`warm` drains (15m idle TTL), `busy` drains (2h hold bound — S4: lease
renewal re-arms it; draining removes lease rows in the same transaction),
`draining` quarantines (30s stop budget), `failed`/`quarantined`/`missing`
are `removed` after 24h retention. `absent` is final per generation: a
claim creates a new-generation row. "any" edges fan out to every non-final
state except the edge's own target.

## Tables

`internal/spawn/migrations` creates `spawn-controller.db` on open:
`spaces`, `passport_revisions` + `_history`, `effective_specs`,
`runtimes` + `_history` (with `generation`), `leases`, `workers` +
`_history`, `image_pins`; every tenant-owned table carries `org` and
`owner`. `spaces`, `passport_revisions` and `effective_specs` serve the
spawn/v1 contract; the remaining tables are not yet wired to HTTP (S4).

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TRUSTED` | - | must be `true`; the service refuses otherwise |
| `HUB_LISTEN` | - | `host:port` the controller binds |
| `HUB_DATA_DIR` | - | own data dir; runtimes spawn under `runtimes/` |
| `HUB_SERVICE_TOKEN` | - | bearer token required on `/v1/*` and forwarded to children; startup refuses without it |
| `HUB_MAX_RUNTIMES` | `32` | upper bound on live children; must be a positive integer |
| `HUB_DATABASE_URL` | `sqlite://<data>/<svc>.db` | service store connection string (`sqlite://` or `postgres://`); startup refuses without it |
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
