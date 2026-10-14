---
description: Proposal — run the job supervisor inside a Linux container on the control network instead of a host process.
last_verified: 2026-10-14
---
# ADR-0037: Supervisor in a Linux container (D12a)

Status: proposed, 2026-10-14. No implementation before owner acceptance.

Requirements: [SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md) R4.
Origin: [ADR-0036](ADR-0036-execution-chain-reliability.md) D12a,
[CHG-0096](../../.work/in-progress/CHG-0096-control-plane-simplification/plan.md) P4a.

## Context and measured evidence

The supervisor is a Windows host process that shells out to `docker` for every
container operation and materializes Windows paths into Linux container mounts.
The 2026-10-13 audit attributed three same-day production defects to the
host-rendered / container-consumed path boundary, and gateway→supervisor traffic
crosses `host.docker.internal`, which depends on Docker Desktop NAT and has
produced admission-time transport failures (recorded `uncertain` outcomes).

After stages 0–3:

- All supervisor→runtime calls are plain HTTP against the runtime's published
  loopback port; supervisor→Docker calls are a fixed set (`inspect`, `run`,
  `stop`, `rm`, `restart`, `logs`) behind the `command` seam in
  `internal/supervisor`.
- The ToolHub container already reaches Docker through a proxy socket
  (`/run/host-services/docker.proxy.sock`); the supervisor would use the same
  pattern restricted to its call list.
- Stage 2 removed the latency arguments that once favored a host process:
  persistence is fsync-bound (~26 ms enqueue→claim), not transport-bound.

## Proposal

1. Package `hub-supervisor` into the control-plane image variant; run it as a
   compose service on the internal control network with the ToolHub socket
   proxy mounted read-only-ish (proxy allowlist: `containers/create|start|stop
   |inspect|logs|restart`, `networks`).
2. Replace the `docker` CLI seam with stdlib `net/http` over the unix socket;
   keep the same 30 s per-call deadline.
3. Render space paths inside Linux: the supervisor mounts
   `spaces/<user>` at a container path; bind-mount sources are emitted as host
   paths via a `HUB_HOST_SPACES` mapping (the proxy translates or the compose
   env supplies the host root).
4. Gateway and runtimes address the supervisor by service name
   (`http://supervisor:8876`); `host.docker.internal` is removed from the
   control path.

## Isolation impact

- The container needs Docker-socket-equivalent power through the proxy; this is
  the same authority the host process has today, scoped by the proxy allowlist.
- `spaces/<user>` mounts stay per-context; the supervisor reads `.control/` and
  writes only runtime container specs — no new credential scope.
- The supervisor token/auth file moves from the host profile into the mounted
  `.control` directory with identical permission checks.
- No provider credentials enter the supervisor container; it never runs
  user-space tooling.

## Migration and rollback

- Migration: `hubctl up` renders the service; first start imports the existing
  `.control/supervisor.json` unchanged (state format does not move). The host
  Scheduled Task/systemd unit is disabled after the containerized supervisor
  passes one canary cycle.
- Rollback: stop the compose service, re-enable the host service pointing at
  the same `.control` state and `.local` logs. The state file is the single
  source either way; no dual-write window exists because the supervisor is a
  single writer.
- Dev/prod divergence: Windows dev hosts keep the option to run the supervisor
  on the host for debugging (`hubctl supervisor` remains supported).

## Test and chaos plan

- Extend `managed-supervisor-canary` to run against the containerized supervisor.
- Chaos cells `supervisor process @ *` rerun with `docker kill` on the service.
- Contract test: every `command` seam call mapped to an HTTP-over-socket call
  with a recorded golden request set.

## Go / no-go

Go if: the socket-proxy allowlist covers the measured call set with zero
`docker` CLI invocations, `host.docker.internal` disappears from rendered
env, chaos grid stays green, and image adds < 20 MB. No-go if the path-mapping
shim reintroduces a Windows→Linux translation boundary inside the container.
