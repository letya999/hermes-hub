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

## State machines

None - a request-scoped service in this skeleton.

## Tables

None yet. `harness-runtime.db` under the spawned data dir is reserved by the
descriptor.
