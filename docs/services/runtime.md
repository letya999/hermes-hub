---
description: hub-runtime — the agent container process and its relay modes (serve/idle/health, model/toolhub/egress/control relays).
last_verified: 2026-10-31
---

# hub-runtime

`cmd/runtime` → `internal/runtime.Run`. One binary, several modes selected by
argv[1] (`internal/runtime/runtime.go`): `idle`, `gateway`, `prepare`,
`serve`, `health`, `model-relay`, `toolhub-relay`, `control-relay`,
`egress-relay`.

## Modes

| Mode | Role |
|---|---|
| `serve` | Full agent runtime: spawns upstream Hermes, serves the HTTP contract on `HUB_RUNTIME_LISTEN` (default `0.0.0.0:8080`), runs readiness admission, health and event surfaces. |
| `idle` | Parked container: no agent loop, keeps the container warm for fast spawn. |
| `health` | In-container healthcheck (`CMD hub-runtime health` every 30s). |
| `model-relay` | Per-space model switch endpoint on `0.0.0.0:8318` — see services/model-relay.md. |
| `toolhub-relay` | Managed-mode bridge so agent-net code reaches the real ToolHub. |
| `control-relay` | Control-plane relay for managed spaces. |
| `egress-relay` | Allowlisted forward proxy for the managed agent network — see services/cell-proxy.md. |

## Container contract

Rendered by `internal/stack/compose.go`: image `hermes-hub:0.5.0-<env>`,
`read_only`, `cap_drop: ALL`, `no-new-privileges`, `shm_size 1gb`, tmpfs `/tmp`,
`init: true`, `stop_grace_period: 30s` (inner gateway freezes its event loop
up to ~15s on SIGTERM; default grace would log false UNCLEAN deaths).

In `managed` capability mode the runtime additionally gets read-only tmpfs
over `/state/hermes/{skills,hooks,plugins,skill-bundles,scripts,bin,node,lsp}`
— the agent cannot tamper with mounted capability code — and moves onto the
per-space `managedAgentNetwork` with no default route.

The effective Hermes config is materialized on the host
(`generated/hermes-effective.<env>.yaml` — source config plus self-service
and ToolHub MCP entries) and mounted read-only over the agent-writable
`/state/hermes/config.yaml`: the agent cannot add `mcp_servers` from inside
the runtime; registration goes through ToolHub onboarding only. Rendered and
spawned runtimes also mount `global_skills_dir` (default `config/skills`)
read-only at `/opt/hub/skills` — without it the onboarding skill is
invisible and installs regress to uncontrolled terminal/git operations.

Interrupted runs keep artifact attribution via the
`/state/runstarts/<runID>` marker whose mtime preserves the artifact scan
window when observation resumes (SPEC-0037).

`command` is `serve`, or `idle` when the space has no telegram feature and
native cron is migrated.

## Upstream boundary

Hermes is an upstream dependency pinned by ADR-0012 (commit
`869228cab4a8276d3b4c78da9d9939670c47bd0f`, version 0.21.0). The adapter
implements only the pinned surface: `POST /v1/runs` with `Idempotency-Key`,
`/api/sessions`, `/v1/runs/{run_id}/events` (SSE). Never patch Hermes; a
contract bump is a new ADR + spec.

## Dev workflow

In a dev stack, `hubctl dev-rebuild` produces `hermes-hub-dev-<user>-runtime:
local` and swaps the container in place; supervised spaces pick it up on next
spawn. A full `hubctl up` returns Compose services to base images.
