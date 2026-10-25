---
description: supervisor — per-space runtime spawner/manager; owns warm pools, docker exec agent-tools path, control channel.
last_verified: 2026-10-25
---

# supervisor

`cmd/supervisor` → `internal/supervisor`. The per-space runtime manager when
`ExecutionMode == "supervisor"`; deployed as the `supervisor` compose service
(`hubctl supervisor`) on the `supervisor-control` network plus `default` and
the shared space network.

## Surface

- `--spaces /spaces --env <env> --runtime-image hermes-hub:0.4.0-<env>`
- `--supervisor-listen 0.0.0.0:8876` (inside the stack; clients use
  `http://supervisor:8876` — `s.SupervisorURL`)
- `--supervisor-allow-wildcard-listen` (explicit opt-in to bind 0.0.0.0)
- `--warm-ttl` (`supervisor.DefaultWarmTTL` — idle runtime retention)
- `--watch` (space config watching)
- Auth: `HUB_SUPERVISOR_AUTH` env or `--auth-file` (spaces can carry a
  `supervisor.auth`-provisioned token; compose injects the env reference)

## Mounts

`docker.sock` (the only service besides toolhub that may touch it), the parent
spaces dir at `/spaces`, project `config/` read-only, and the host
`.local/supervisor.log` bind for diagnostics.

## Responsibilities

- Spawn/stop `hermes-hub-agent-<user>-<env>` runtime containers from the
  runtime image; warm pool keyed by runtime identity + policy version.
- Own the agent-tools executor naming contract: `HUB_AGENT_EXEC_MODE=
  supervisor` makes toolhub run agent-tools via `docker exec` into the owning
  runtime — the only control-plane path that never crosses the agent network.
- Serve `HUB_RUNTIME_SUPERVISOR_URL` for communication-hub and toolhub when
  set; when unset the gateway talks to `hermes-runtime:8080` directly.
- Every spawned runtime joins the shared network where `toolhub`,
  `credential-broker` and `cliproxy` resolve to the single instances.
- Route every control-plane call (`/v1/jobs`, `/v1/resume`, `/v1/control`,
  `/v1/self-env`, `/v1/restart`, `/v1/restart-request`) to the correct
  user's spawned runtime by the job's durable identity; the
  restart-after-delivery hook carries the same identity so the spawned
  Hermes bounces per turn. A restart for an absent/stopped runtime is a
  successful no-op — next lease spawns fresh. Secondary spaces
  (`infra: false`) render no `hermes-runtime` service; the infra owner drops
  it when `HUB_RUNTIME_SUPERVISOR_URL` is configured.

## Boundaries

- Owns Docker lifecycle for agent runtimes only; connector cells belong to
  toolhub + workload-controller.
- `restart: unless-stopped`, `stop_grace_period: 30s` — a supervisor restart
  does not kill spawned runtimes; they re-register.
