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
  keyed by content digest and served to consumers as policy slices; its
  per-leaf layer attribution is stored with it and served by `explain`.
- `Lease` - one hold row on a runtime; the lease count is the row count.
- `Backup` - one space's backup bookkeeping row imported from the v0
  supervisor (last success/error, archive, retention, last drill).

## Does not own

- Message delivery and channels - owned by channel-hub.
- The agent harness itself - owned by harness-runtime.
- Admission and persistent user data - owned by their services.

## API

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /healthz` | launcher readiness probe | read | - |
| `POST /v1/spaces/{space}/runtimes` | claim the (space, agent) runtime on its stored effective digest: returns the runtime row (its `address` is the endpoint the caller forwards to), its generation and a fresh lease | write | - |
| `POST /v1/leases/{id}/renew` | heartbeat: re-arm the held runtime's busy bound | write | `id` |
| `DELETE /v1/leases/{id}?org=&owner=` | drop the hold; the last lease moves the runtime back to warm | write | `id` |
| `GET /v1/runtimes?org=&owner=` | list the tenant's runtime rows, newest generation first | read | - |
| `POST /v1/messages?org=&owner=` | process-mode first message: claim, hold, forward, release, relay the reply | write-local | `space` + `agent` |
| `POST /v1/spaces` | register the tenant space | write | `idempotency_key` |
| `POST /v1/spaces/{space}/layers/{layer}/revisions` | submit one layer's documents as a draft revision | write | `idempotency_key` |
| `POST /v1/spaces/{space}/revisions/{id}/validate` | compile the draft against the other layers | write | `idempotency_key` |
| `POST /v1/spaces/{space}/activations` | cut a revision set (one or more ids, at most one per layer) in one transaction; the would-be active set must compile or nothing moves | write | `idempotency_key` |
| `POST /v1/spaces/{space}/apply` | submit + validate + compile every changed layer and activate the set in one call; `dry_run` stops before activation; the cut commits on the `base_digest` CAS | write | `idempotency_key` |
| `GET /v1/spaces/{space}/settings` | reviewed self-settings: the published allowlist values | read | - |
| `POST /v1/spaces/{space}/settings` | apply one config leaf on the base digest CAS and activate the patched user layer | write | `idempotency_key` |
| `GET /v1/spaces/{space}/revisions?layer=` | list revisions, optionally by layer | read | - |
| `GET /v1/spaces/{space}/effective` | the active layers' compiled spec | read | - |
| `GET /v1/spaces/{space}/explain?org=&owner=` | per-leaf provenance of the effective spec: value plus the layer that set it | read | - |
| `GET /v1/spaces/{space}/policy/{service}?digest=` | immutable policy slice for one consumer | read | - |
| `POST /v1/spaces/{space}/runtimes/drain` | drain the space's live runtime rows (optional `agent` scopes one lane); respawn/replace forces a fresh generation | write | - |
| `POST /v1/spaces/{space}/workers` | claim a worker cell (tenant, cell key, image digest): starting -> busy row plus a lease | write | - |
| `POST /v1/spaces/{space}/workers/drain` | drain workers filtered by binding/job (backs ToolHub's `/cli-release`) | write | - |
| `POST /v1/workers/{id}/renew` | worker lease heartbeat | write | `id` |
| `POST /v1/workers/{id}/release` | drop the worker lease | write | `id` |
| `POST /v1/workers/{id}/exec` | lease-guarded bounded exec inside the worker (env by name, never argv) | write | `id` |

`/v1/spaces*`, `/v1/runtimes` and `/v1/leases*` are the spawn/v1 contract
(`contracts/spawn/v1`): tenant (`org`, `owner`) travels in the body on
writes and the query on reads and DELETE; the policy route answers
`Cache-Control: public, max-age=31536000, immutable` with `ETag` = digest.
`/v1/messages` stays the process-mode first-message path with the same
explicit tenant in the query — identity is never derived from a header.

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
A `starting` row that becomes ready under a revoked policy digest —
activation drains only `warm`/`busy` — warms into `draining` in the same
transaction; the claim re-plans and the sweep drains any `warm`/`busy`
row whose digest stops matching as backstop.

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
`_history`, `effective_specs` (with `attribution`), `runtimes` + `_history`
(with `generation`, `policy_digest`, `image_digest`, `backend_id`,
`address`), `leases`, `workers` + `_history`, `image_pins`, `backups`;
every tenant-owned table carries `org` and `owner`. `spaces`,
`passport_revisions` and `effective_specs` serve the spawn/v1 contract;
`runtimes` and `leases` serve the claim/lease contract surface and the
message path. `image_pins` and `backups` receive the v0 import; `workers`
serves the `/v1/spaces/.../workers` cell surface.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_BACKEND` | `process` | launch backend for runtimes: `process` (trusted dev default, children are local processes) or `docker` (compose container authority) |
| `HUB_TRUSTED` | - | trust flag: `process` mode requires `true`; `docker` mode accepts `true`, or `false` only with `HUB_CONTAINER_RUNTIME=runsc` (X6) |
| `HUB_CONTAINER_RUNTIME` | `runc` | OCI runtime spec field docker-mode runtimes launch under; untrusted installs must set `runsc` |
| `HUB_RUNTIME_IMAGE` | `harness-runtime` | image (docker) or binary name (process) claims launch |
| `HUB_LISTEN` | - | `host:port` the controller binds |
| `HUB_DATA_DIR` | - | own data dir; runtimes spawn under `runtimes/` |
| `HUB_SERVICE_TOKEN` | - | bearer token required on `/v1/*` and forwarded to children; startup refuses without it |
| `HUB_MAX_RUNTIMES` | `32` | upper bound on live runtime rows per tenant; must be a positive integer |
| `HUB_MAX_WORKERS` | `64` | upper bound on live worker rows per tenant (2× default); must be a positive integer |
| `HUB_SWEEP_INTERVAL` | `5s` | deadline-sweep and reconcile pass interval; must be a positive duration |
| `HUB_DATABASE_URL` | `sqlite://<data>/<svc>.db` | service store connection string (`sqlite://` or `postgres://`); startup refuses without it |
| `HUB_SPACES_ROOT` | - | spaces root the claim-time spec builder binds runtime/workspace mounts from and the settings edit patches authored files under |
| `HUB_PLATFORM_ROOT` | spaces root's parent | platform config tree (`config/skills`, `config/SOUL.md`, `config/plugins`) for spec materialization; containerized deploys set the container path |
| `HUB_RUNTIME_PORT` | `8080` | workload's service port the control relay forwards to; the relay's loopback publish is inert on the internal agent network, so containerized callers use the relay name on the control network (see `HUB_DOCKER_HOST_ROOT`) |
| `HUB_RUNTIME_MEMORY` | `2g` | managed runtime memory limit |
| `HUB_RUNTIME_CPUS` | `2` | managed runtime CPU limit |
| `HUB_RUNTIME_PIDS` | `1024` | managed runtime pids limit |
| `HUB_RUN_STALL_TIMEOUT` | - | run watchdog passed into spawned runtimes when set |
| `HUB_DOCKER_HOST_ROOT` | - | set when the backend itself runs containerized: mount sources are daemon-side paths, local stat is skipped and claims address runtimes by relay name on the control network |
| `HUB_CELL_STATE_ROOT` | `<data>/cells` | worker scratch root: sidecar payloads and docker-mode cell scratch files |
| `HUB_CELL_TOOLS_IMAGE` | - | CLI-tools image a worker mounts as `/tools/<i>`; required for cells to enable |
| `HUB_CELL_PROXY_IMAGE` | - | egress/cred proxy image for worker sidecars |
| `HUB_CELLINIT` | - | cellinit helper binary mounted into workers |
| `HUB_CELL_ENV` | - | environment tag carried into worker spec names |
| `HUB_CELL_RUNTIME` | - | OCI runtime worker containers launch under |
| `HUB_CELL_SECCOMP` | - | seccomp profile applied to workers |
| `HUB_CELL_PER_PRINCIPAL_UID` | `false` | `true` maps one uid per principal inside workers |
| `HUB_CATALOG_ROOT` | - | platform checkout or image path the pinned catalog resolves from; `hubctl` injects the launcher's resolved root when it carries the release manifest — otherwise the image's baked tree is used |
| `HUB_MANAGED_MODEL_URL` | - | passed through to spawned runtimes |
| `HUB_MANAGED_MODEL_ID` | - | passed through to spawned runtimes |
| `HUB_FEATURES` | - | passed through to spawned runtimes |
| `HUB_IMPORT_V0_DIR` | - | v0 control directory (`supervisor.json`, `backups.json`, the journals); empty disables the one-time import |
| `HUB_IMPORT_SPACES_DIR` | - | v0 spaces root holding `generated/passports/current` and `generated/image-pins.json` per space |
| `HUB_IMPORT_DRY_RUN` | `false` | `true` prints the import report (counts and ids only) and writes nothing |
| `HUB_UNO_URL`, `HUB_UNO_TOKEN`, `HUB_UNO_IMAGE`, `HUB_UNO_PROXY_HOST` | - (proxy host `127.0.0.1`) | firecracker backend config — compile-only, not selectable until M40.15 |
| `HUB_KUBECONFIG`, `HUB_KUBERNETES_PULL_SECRETS`, `HUB_KUBERNETES_*`, `HUB_RUNTIME_CLASS_NAME` | namespace `hermes-hub-<env>`, storage `10Gi`, release `hermes-hub` | kubernetes backend config — compile-only, not selectable until M40.15 |

Only the three pass-through names plus `TZ` reach spawned children from the
operator environment; the controller itself sets `HUB_TRUSTED`,
`HUB_SERVICE`, `HUB_LISTEN`, `HUB_DATA_DIR`, `HUB_DATABASE_URL` and
`HUB_SERVICE_TOKEN` on each child. Runtime binaries are resolved next to the
controller's own binary.

## v0 import

With `HUB_IMPORT_V0_DIR` set the importer runs once at startup, before the
socket opens, only while the spawn store holds none of the v0 space rows —
a second start is a no-op. It reads the v0 files, never writes them:

- each discovered space -> a `spaces` row (org `local`, owner = space name)
  plus one `active` user-layer revision and its effective spec, imported
  through the same Submit/Validate/Activate path a client uses — the
  emitted revision is re-layered as authored content (envelope,
  `workspace.tree` and the pinned `tools.core.platform` dropped), so the
  imported spec is a real compile, not a copy;
- `generated/image-pins.json` -> `image_pins` rows;
- `backups.json` -> `backups` rows;
- v0 runtimes are NOT imported as live rows (they are v0 processes this
  service does not own); a generation baseline per (space, agent) keeps
  new generations monotonic;
- `transitions.jsonl` and `signals.jsonl` are counted in the report and
  dropped: no reader exists in this milestone (S3 — no table without a
  reader).

`HUB_IMPORT_DRY_RUN=true` prints the same report — counts and ids only,
no document contents — and writes nothing.

## Extension point

`Backend` (`Start(spec)`, `Stop(id)`, `Inspect(id)`, `List()`) on a resolved
`Spec` (tenant, image, command, mounts, env, network edges, limits, OCI
runtime); backends apply the Spec or refuse it, they never decide policy.
Providers: `process` (trusted single-user dev, wired to the message path)
and `docker` (docker CLI on the same Spec, moved from the v0 supervisor;
the shared conformance suite lives in `internal/backend/backendtest`).
`HUB_BACKEND` selects the provider at startup; in the compose stack the
service runs `docker` with `HUB_RUNTIME_IMAGE` set to the agent image tag.
Kubernetes and firecracker implementations exist in `internal/backend` but
are compile-only — nothing selects them until M40.15.

## When a dependency is disabled

| Dependency | Behaviour when disabled |
|---|---|
| harness-runtime | message requests fail `unavailable`; runtimes never start |

## Tools it executes

None.
