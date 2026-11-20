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

## State machines

| From | Event | To | Deadline / sweeper |
|---|---|---|---|
| - | ensure | starting | 10s healthz deadline, spawn fails |
| starting | healthz ok | running | runtime serves |
| running | child exit | exited | removed from registry |
| running | stop / shutdown | - | SIGTERM or CTRL_BREAK, 10s WaitDelay |

## Tables

None yet. The runtime registry is in process memory in this skeleton;
`spawn-controller.db` is reserved by the descriptor for the durable Runtime
record that arrives with M40.3.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TRUSTED` | - | must be `true`; the service refuses otherwise |
| `HUB_LISTEN` | - | `host:port` the controller binds |
| `HUB_DATA_DIR` | - | own data dir; runtimes spawn under `runtimes/` |
| `HUB_DATABASE_URL` | `sqlite://<data>/<svc>.db` | reserved store connection string |
| `HUB_MANAGED_MODEL_URL` | - | passed through to spawned runtimes |
| `HUB_MANAGED_MODEL_ID` | - | passed through to spawned runtimes |
| `HUB_FEATURES` | - | passed through to spawned runtimes |

Only these names plus `TZ` reach spawned children; runtime binaries are
resolved next to the controller's own binary.

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
