---
description: supervisor — per-space runtime spawner/manager; owns warm pools, docker exec agent-tools path, control channel.
last_verified: 2026-10-26
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
- `/v1/runtimes` entries expose a read-only `consumers` projection (lease
  kind, owner class, expiry) and `wait_reason` for expected waits — parked
  approvals/streams are reported, not remediated. Genuine anomalies append
  one bounded line per class to `signals.jsonl` under the supervisor state
  dir and keep a persisted per-runtime incident record.
- One bounded `docker stats --no-stream` call per watchdog sweep samples
  owned containers' CPU/memory; sweep cost is reported through
  `WatchdogMetrics` (in-memory counters).
- `HUB_MAX_RUNTIMES_PER_PRINCIPAL` caps live runtimes one principal may own;
  unset means the global `MaxConcurrent` semaphore is the only cap.
- Personal supervisor (#328): opt-in via `HUB_PERSONAL_MODEL` +
  `HUB_MANAGED_MODEL_URL` (+ `OPENAI_API_KEY` bearer). Incident recording
  queues one coalesced wake per scope on a notify channel; `personalLoop`
  drains it — no retained resource, no actionable incident means no model
  call and no polling. One in-flight `Decide` per scope; the request carries
  allowlisted fields only (ids, generation, state, incident classes, lease
  consumers, remaining mandate). Decisions are narrow and validated
  server-side: `inspect|renew|pause|restart|escalate` — renew bounded to
  30 minutes ahead and scope-local leases only, restart marks the runtime
  for the next *verified* remediation sweep (ownership/lease checks still
  apply), pause sets the idle deadline, escalate records a durable
  `escalated` incident for #329 notice. Stale-generation decisions and
  `Decide` errors apply nothing; `review` follow-ups cap at 2 per chain and
  the drain itself is bounded at 64. Every decision lands one
  `personal-applied`/`personal-denied` line in `signals.jsonl`.

## Boundaries

- Owns Docker lifecycle for agent runtimes only; connector cells belong to
  toolhub + workload-controller.
- `restart: unless-stopped`, `stop_grace_period: 30s` — a supervisor restart
  does not kill spawned runtimes; they re-register.
