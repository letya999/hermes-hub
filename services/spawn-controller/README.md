# spawn-controller

## Responsibility

Owns space compute: claims bind tenant, policy digest and image digest to
a durable runtime row per (space, agent) generation; the message path
holds a lease on the claimed runtime while traffic is forwarded to it.
The warm pool, drain/quarantine lifecycle and backend reconciliation are
the service's own — no runtime state lives in memory.

## Owns

- `Runtime` - one space workload's durable row: id, space, agent,
  generation, policy/image digests, backend id, address, state.
- `Space` - the tenant-owned managed-space registry row; activation
  stores the effective digest on it and drains runtimes bound to the old
  digest in the same transaction.
- `PassportRevision` - one submitted layer document set and its lifecycle;
  `pruned` is the retention tombstone past the keep slack.
- `EffectiveSpec` - the immutable compiled bundle the active layers produce,
  keyed by content digest and served to consumers as policy slices.
- `Lease` - one hold row on a runtime; the lease count is the row count.

## Does not own

- Message delivery and channels - owned by channel-hub.
- The agent harness itself - owned by harness-runtime.
- Admission, passports and persistent user data - owned by their services.

## API

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /healthz` | launcher readiness probe | read | - |
| `GET /v1/runtimes?org=&owner=` | list the tenant's runtime rows, newest generation first | read | - |
| `POST /v1/messages?org=&owner=` | claim the (space, agent) runtime on its stored effective digest, hold a lease, forward the message, release, relay the reply | write-local | `space` + `agent` |
| `POST /v1/leases/{id}/renew?org=&owner=` | heartbeat: re-arm the held runtime's busy bound | write | `id` |
| `POST /v1/spaces` | register the tenant space | write | `idempotency_key` |
| `POST /v1/spaces/{space}/layers/{layer}/revisions` | submit one layer's documents as a draft revision | write | `idempotency_key` |
| `POST /v1/spaces/{space}/revisions/{id}/validate` | compile the draft against the other layers | write | `idempotency_key` |
| `POST /v1/spaces/{space}/activations` | cut a revision set (one or more ids, at most one per layer) in one transaction; the would-be active set must compile or nothing moves | write | `idempotency_key` |
| `GET /v1/spaces/{space}/revisions?layer=` | list revisions, optionally by layer | read | - |
| `GET /v1/spaces/{space}/effective` | the active layers' compiled spec | read | - |
| `GET /v1/spaces/{space}/policy/{service}?digest=` | immutable policy slice for one consumer | read | - |

`/v1/spaces*` is the spawn/v1 contract (`contracts/spawn/v1`): tenant
(`org`, `owner`) travels in the body on writes and the query on reads; the
policy route answers `Cache-Control: public, max-age=31536000, immutable`
with `ETag` = digest. `/v1/messages`, `/v1/runtimes` and
`/v1/leases/{id}/renew` take the same explicit tenant in the query —
identity is never derived from a header.

`/v1/*` requires `Authorization: Bearer $HUB_SERVICE_TOKEN` (constant-time
compare): an `Origin` header is refused 403, missing or wrong bearer 401, a
non-JSON POST 415. `/healthz` stays open for the launcher probe.

## State machines

`RuntimeMachine` (`internal/spawn`) drives the `runtimes` rows the
message path claims: `starting` (30s start budget -> `failed`), `warm`
(15m idle TTL -> `draining`), `busy` (2h hold bound -> `draining`; the
`renew` heartbeat re-arms it), `draining` (30s stop budget ->
`quarantined`; `stopped` -> `absent` only on a confirmed stop), and
`failed`/`quarantined`/`missing` (24h retention -> `absent`). `absent` is
final per generation: a claim creates the next generation's row. Moving
out of `warm`/`busy` deletes the row's leases in the same transaction.

`PassportRevisionMachine` serves `/v1/spaces`: `draft` expires to
`rejected` (10m), `validated` expires to `rejected` (1h), `active` and
`superseded` re-arm only (30d `retain` self-loop), and activation prunes
the oldest superseded rows past the keep slack (3) to `pruned`.
`WorkerMachine` (the runtime table without warm) shares the sweep.

The sweeper goroutine (`HUB_SWEEP_INTERVAL`) fires deadline events first
and then, outside any transaction, applies the backend effects and the
reconcile pass — the same pass runs at boot before the socket opens, so
a restarted controller recovers its runtimes from the rows.

## Tables

`internal/spawn/migrations` creates `spawn-controller.db` on open:
`spaces` (with `environment`, `effective_digest`), `passport_revisions` +
`_history`, `effective_specs`, `runtimes` + `_history` (with `generation`,
`policy_digest`, `image_digest`, `backend_id`, `address`), `leases`,
`workers` + `_history`, `image_pins`; every tenant-owned table carries
`org` and `owner`. `spaces`, `passport_revisions` and `effective_specs`
serve the spawn/v1 contract; `runtimes` and `leases` serve the message
path and `/v1/runtimes`. `workers` and `image_pins` are not yet wired
(S5+).

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TRUSTED` | - | must be `true`; the service refuses otherwise |
| `HUB_LISTEN` | - | `host:port` the controller binds |
| `HUB_DATA_DIR` | - | own data dir; runtimes spawn under `runtimes/` |
| `HUB_SERVICE_TOKEN` | - | bearer token required on `/v1/*` and forwarded to children; startup refuses without it |
| `HUB_MAX_RUNTIMES` | `32` | upper bound on live runtime rows per tenant; must be a positive integer |
| `HUB_SWEEP_INTERVAL` | `5s` | deadline-sweep and reconcile pass interval; must be a positive duration |
| `HUB_DATABASE_URL` | `sqlite://<data>/<svc>.db` | service store connection string (`sqlite://` or `postgres://`); startup refuses without it |
| `HUB_CATALOG_ROOT` | - | platform checkout or image path the pinned catalog resolves from; `hubctl` injects the launcher's resolved root when it carries the release manifest — otherwise the image's baked tree is used |
| `HUB_MANAGED_MODEL_URL` | - | passed through to spawned runtimes |
| `HUB_MANAGED_MODEL_ID` | - | passed through to spawned runtimes |
| `HUB_FEATURES` | - | passed through to spawned runtimes |

Only the three pass-through names plus `TZ` reach spawned children from the
operator environment; the controller itself sets `HUB_TRUSTED`,
`HUB_SERVICE`, `HUB_LISTEN`, `HUB_DATA_DIR`, `HUB_DATABASE_URL` and
`HUB_SERVICE_TOKEN` on each child. Runtime binaries are resolved next to the
controller's own binary.

## Extension point

`Backend` (`Start(spec)`, `Stop(id)`, `Inspect(id)`, `List()`) on a resolved
`Spec` (tenant, image, command, mounts, env, network edges, limits, OCI
runtime); backends apply the Spec or refuse it, they never decide policy.
Providers: `process` (trusted single-user dev, wired to the message path)
and `docker` (docker CLI on the same Spec, moved from the v0 supervisor;
the shared conformance suite lives in `internal/backend/backendtest`).
Kubernetes and firecracker arrive with M40.15.

## When a dependency is disabled

| Dependency | Behaviour when disabled |
|---|---|
| harness-runtime | message requests fail `unavailable`; runtimes never start |

## Tools it executes

None.
