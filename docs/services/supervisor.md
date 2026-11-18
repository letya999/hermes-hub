---
description: supervisor — per-space runtime spawner/manager; owns warm pools, docker exec agent-tools path, control channel.
last_verified: 2026-11-13
---

# supervisor

The supervisor starts through `setpriv`, immediately drops to UID 10001 with the
host group and retains only `FOWNER` in its effective, ambient and bounding sets.
This maintains host-owned file modes without changing the UID that writes shared
private registry/state files. Bootstrap UID/GID/capability permissions are dropped
before `hubctl` starts. Privilege escalation stays disabled and the root filesystem
stays read-only. Other services retain UID 10001 with no capabilities.
Spawned runtimes and isolated watch scripts also use the controller's group,
matching Compose, so group-readable published config/watch binds remain readable.
No recursive chown or mode relaxation runs.

Published revision directories/documents use 0750/0440 and the current pointer
0640 for the controller group. The mutable generated model-registry directory
uses 0770 so a different controller UID can atomically replace its 0640 projection.
These paths contain configuration; credentials retain their separate private modes.
An already bundled/published legacy skill does not require opening unrelated
private draft contents during rendering.

For the built-in `settings_edit`, ToolHub completes registry admission before
calling this authenticated publisher without holding the registry lock. The
publisher checks the current edit grant and base revision and derives the new
rights itself; holding the call lock while deriving those rights deadlocks.
Runtime reconciliation from this publisher calls the local manager directly,
because its journal intentionally blocks external PDP admission until completion.
The external respawn endpoint still requires the caller's restart right.

`cmd/supervisor` → `internal/supervisor`. The per-space runtime manager when
`ExecutionMode == "supervisor"`; deployed as the `supervisor` compose service
(`hubctl supervisor`) on the `supervisor-control` network plus `default` and
the shared space network.

## Surface

- `--spaces /spaces --env <env> --runtime-image hermes-hub:0.5.0-<env>`
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

- Spawn/stop managed `hermes-context-<hash>` runtime containers from the
  runtime image; warm pool keyed by principal/context/mode + policy version.
  Managed spawn verifies the published revision again after materializing
  its files and passes that revision to the runtime preflight.
  New managed jobs share one `gateway` runtime per owner, up to five active
  conversations. The sixth waits; existing job records with legacy lane modes
  still route to their recorded runtime for recovery.
- Own the agent-tools executor naming contract: `HUB_AGENT_EXEC_MODE=
  supervisor` makes toolhub run agent-tools via `docker exec` into the owning
  runtime — the only control-plane path that never crosses the agent network.
  ToolHub acquires a lifecycle lease before dispatch, including a cold start,
  then releases it after the call even when the business context is cancelled.
  Startup runs outside the admission registry fence; tool authority is checked
  again before execution. The lease receipt must match the enrolled owner,
  context, container and runtime generation. Startup failure is reported before
  effect; a post-dispatch channel failure remains uncertain.
- Serve `HUB_RUNTIME_SUPERVISOR_URL` for communication-hub and toolhub when
  set; when unset the gateway talks to `hermes-runtime:8080` directly.
- Managed runtimes join their private agent network. ToolHub, model and egress
  relays reach the shared control-plane services through their narrow routes.
  Sish exposure uses the egress relay's exact reviewed SSH host:port CONNECT
  exception; no broad TCP egress or direct fallback is granted.
- Route every control-plane call (`/v1/jobs`, `/v1/resume`, `/v1/control`,
  `/v1/self-env`, `/v1/skill-ref-lease`, `/v1/restart`, `/v1/restart-request`,
  `/v1/respawn`) to the correct
  user's spawned runtime by the job's durable identity; the
  restart-after-delivery hook carries the same identity so the spawned
  Hermes bounces per turn. A restart for an absent/stopped runtime is a
  successful no-op — next lease spawns fresh. `/v1/respawn` is the
  operator's recreate path: a deferred restart remounts files but keeps the
  container's baked env, so an idle/ready container is destroyed and
  re-spawned from the published revision — that is how env-carried passport
  config (`config.ports`, feature flags) actually takes effect. A busy or
  absent runtime answers `scheduled:false` and converges on its next natural
  spawn. Secondary spaces
  (`infra: false`) render no `hermes-runtime` service; the infra owner drops
  it when `HUB_RUNTIME_SUPERVISOR_URL` is configured.
  Skill ref leases require the exact principal, context, runtime and policy
  of a running generation. Spawn failures return an allowlisted `class`:
  `hold`, `backoff`, `policy_rotation`, `spawn` or `unavailable`.
- `GET /v1/jobs/<id>` serves the durable job record as a run-context
  projection for lifecycle consumers (SPEC-0098 M2): beside `job_id`,
  `identity` and `status`/`outcome` it reports `run_id`, `session_id`,
  `scope_id`, `trigger`, `channel`, the supervisor's own `environment`, the
  job's dispatch `runtime_generation` and the binding's live
  `current_generation` read under the manager lock. Every field is durable
  state or manager config — the caller's job id selects the record and fills
  nothing in it, so a superseded generation or a compacted record reports
  itself rather than answering as live.
  The same status projection supplies the capture verifier with the stored
  user, sensitivity/provenance flags and SHA-256 hashes of request and reply
  text. Prompt text itself is absent from the durable request record. After
  terminal compaction a minimal historical capture context retains those
  fields and the exact run ID without retaining either text body.
- Before dispatching an admitted private turn the supervisor asks ToolHub
  for pre-turn memory (`POST /v1/memory-context` under the supervisor bearer
  with `X-Hub-Principal`, `memory_context.go`): the durable admission id and
  the request text hash prove the exact run; a group, sensitive or
  background run skips the call, and any failure — no ToolHub, a denial, a
  malformed or oversized answer — injects the explicit
  "Memory context unavailable" marker, never a local vault read.
- `/v1/runtimes` entries expose a read-only `consumers` projection (lease
  kind, owner class, expiry) and `wait_reason` for expected waits — parked
  approvals/streams are reported, not remediated. Genuine anomalies append
  one bounded line per class to `signals.jsonl` under the supervisor state
  dir and keep a persisted per-runtime incident record.
- The per-space supervision policy `runtime.<env>.policy.json` (rendered
  from `config.supervision`) is re-read on every decision: `idle_stop`
  bounds warm idle retention at lease release and the stale-lease sweep,
  `restart: never` and `watchers.stack-health.protected` keep the runtime
  observed but suppress remediation, `watchers.stack-health.every` throttles
  the health-probe cadence. A missing or malformed file degrades to the
  deployment defaults — no respawn is needed for a policy change to apply.
- One bounded `docker stats --no-stream` call per watchdog sweep samples
  owned containers' CPU/memory; sweep cost is reported through
  `WatchdogMetrics` (in-memory counters). The opt-in `watchers.metrics`
  scrapes each runtime's `GET /v1/status` on its `every` cadence and opens
  an incident class (`cpu_sustained`, `memory_sustained`,
  `latency_sustained`, `http_5xx_sustained`, `session_saturated`,
  `mcp_saturated`) only after three consecutive breaches with hysteresis —
  `protected` keeps it observing without waking the personal supervisor,
  and a scrape failure is a skipped observation, never an incident.
- The watchdog runs two loops on the same tick interval: a detection sweep
  (orphans, health probes, lease/uncertain expiry, compaction, reap) and a
  reconcile loop (spawn/recovery Ensure). A slow Ensure can never delay
  crash/health detection for the rest of the fleet; each pass stays bounded
  at 150s and per-runtime locks serialize remediation against Ensure.
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
