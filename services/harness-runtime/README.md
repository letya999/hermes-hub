# harness-runtime

## Responsibility

Answers messages for its (space, agent) through the managed model endpoint;
spawned and supervised by spawn-controller.

## Owns

- Its own data directory `runtimes/<key>` under the controller's data dir.

## Does not own

- Runtime lifecycle - owned by spawn-controller.
- Model routing, limits and cost - owned by model-gateway.
- The agent loop (Hermes, sessions, tools) - arrives with M40.4; this
  skeleton only proves the process-mode message path.

## API

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /healthz` | spawn health probe | read | - |
| `POST /v1/messages` | answer one user message via the managed model endpoint | write-local | - |

`/v1/*` requires `Authorization: Bearer $HUB_SERVICE_TOKEN` (constant-time
compare): an `Origin` header is refused 403, missing or wrong bearer 401, a
non-JSON POST 415. `/healthz` stays open for the launcher probe.

## State machines

None - a request-scoped service in this skeleton.

## Tables

None yet. `harness-runtime.db` under the spawned data dir is reserved by the
descriptor.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TRUSTED` | - | must be `true`; the service refuses otherwise |
| `HUB_LISTEN` | - | `host:port` assigned by spawn-controller |
| `HUB_SERVICE_TOKEN` | - | bearer token required on `/v1/*`; startup refuses without it |
| `HUB_MANAGED_MODEL_URL` | - | OpenAI-compatible model endpoint; empty refuses the message |
| `HUB_MANAGED_MODEL_ID` | - | model name sent in completion requests |

## Extension point

None.

## When a dependency is disabled

| Dependency | Behaviour when disabled |
|---|---|
| model endpoint (`HUB_MANAGED_MODEL_URL` empty) | `POST /v1/messages` returns a localized refusal; `/healthz` still answers so the launcher can observe the process |

## Tools it executes

None.
