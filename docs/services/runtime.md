---
description: hub-runtime — the agent container process and its relay modes (serve/idle/health, model/toolhub/egress/control relays).
last_verified: 2026-11-19
---

# hub-runtime

One managed owner now has one Hermes process and one local MCP proxy for up to
five concurrent conversations. The pinned Hermes client carries its run ID in
the `tools/call` `_meta` field. The runtime resolves it to the admitted job
and attaches control-issued credentials for the destination services. A sixth job waits, and one
conversation remains FIFO. Older `gateway-2` through `gateway-5` job records
remain routable during recovery, but new work uses `gateway`.

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
Before starting Hermes, runtime preflight compares the host-pinned
`HUB_PASSPORT_REVISION` with `/config/revision` before and after config
attestation. A mismatched or absent view refuses startup during apply.

The effective Hermes config is materialized on the host
(`generated/hermes-effective.<env>.yaml` — source config plus self-service
and ToolHub MCP entries) and mounted read-only over the agent-writable
`/state/hermes/config.yaml`: the agent cannot add `mcp_servers` from inside
the runtime; registration goes through ToolHub onboarding only. Rendered and
spawned runtimes also mount `global_skills_dir` (default `config/skills`)
read-only at `/opt/hub/skills` — without it the onboarding skill is
invisible and installs regress to uncontrolled terminal/git operations.

For managed runs the pinned Hermes MCP URL is the runtime's local `/mcp`
route. The runtime forwards it to ToolHub's stateless `/mcp-run` route through
the existing per-space relay. For each `tools/call`, it reads the client run ID
from `_meta`, looks up the admitted job in its concurrent run map, and stamps
the corresponding headers and audience tokens. It replaces incoming
job/run/authority headers with the values in its own binding. Supervisor
persists `run.admitted` before sending tokens via its private `/v1/jobs/authorization`
route. The authenticated delivery is bound to one job and generation, and
the credentials live in memory. Missing, foreign and terminal runs fail
closed at the enforcement points. An uncertain run retains its local binding
for observation while consumers recheck durable authorization.
The relay defaults to `http://toolhub-control:8090`; isolated fixtures may set
`HUB_TOOLHUB_RELAY_UPSTREAM` to their own fixed HTTP origin.

The bundled `hub-run-auth` Hermes middleware uses the pinned run context
for calls to the model proxy at `127.0.0.1:9917`. The Go proxy adds the
model-audience token. The model and egress relays check live authority as
well as signature and audience. The managed Go executor attaches its egress
token to requests sent through its configured proxy. Clients with no run
correlation fail at that relay, including native callers that still need a
run-scoped transport.

The private tools-daemon executes ToolHub-admitted `web_search` calls through
the pinned Hermes web sidecar and `browser_*` through a prepared Playwright
workload. Supervised Hermes obtains these tools through the authenticated
ToolHub run route. The legacy headed browser sidecar for human noVNC access
does not share the supervised runtime's network.

Interrupted runs keep artifact attribution via the
`/state/runstarts/<runID>` marker whose mtime preserves the artifact scan
window when observation resumes (SPEC-0037).

`command` is `serve`, or `idle` when the space has no telegram feature and
native cron is migrated.

## Upstream boundary

Hermes is an upstream dependency pinned by ADR-0012 (commit
`869228cab4a8276d3b4c78da9d9939670c47bd0f`, version 0.21.0). The adapter
implements only the pinned surface: `POST /v1/runs` with `Idempotency-Key`,
`/api/sessions`, `/v1/runs/{run_id}/events` (SSE). The explicit, pin-checked
overlay in `docker/patches/hermes-mcp-run-identity.patch` forwards Hermes'
per-run approval context into ToolHub MCP call metadata and allows concurrent
calls to its stateless endpoint. ADR-0090 records this narrow exception; any
other upstream change still needs a reviewed decision and contract check.

## Dev workflow

In a dev stack, `hubctl dev-rebuild` produces `hermes-hub-dev-<user>-runtime:
local` and swaps the container in place; supervised spaces pick it up on next
spawn. A full `hubctl up` returns Compose services to base images.
