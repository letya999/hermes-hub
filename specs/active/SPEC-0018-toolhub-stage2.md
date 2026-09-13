---
status: active
title: Dynamic ToolHub stage 2 endpoint, adapters and bounded execution
---
# Dynamic ToolHub stage 2

This specification adds an opt-in execution boundary on top of SPEC-0017. It
does not replace generated Hermes MCP configuration by default.

## Stable endpoint

Each runtime may be given one private streamable-HTTP MCP URL. The endpoint
accepts a bearer token that maps to the existing `identity.Envelope`; the token
is configuration, not a catalog record. Origin-bearing browser requests are
rejected. ToolHive/vMCP URLs are private backend addresses and are never
exposed to Hermes.

The endpoint has one resolver for list and call. `tools/list` projects only
currently authorized bindings. `tools/call` accepts only the projected tool
name and user arguments; it derives binding, connection, credential reference,
policy and backend from trusted state. Authority fields in model arguments are
not interpreted. Both operations re-read current binding, exact owner,
connection revision, credential revision and policy before backend admission.

## Backend boundary

The initial backend adapter speaks the stable MCP streamable-HTTP contract to a
private ToolHive local vMCP URL. ToolHive owns workload execution, transport
adaptation and egress policy; Go owns identity, binding and current-state
authorization. The adapter does not import ToolHive internals or implement a
second aggregator. The Go client disables the optional standalone SSE listener
because ToolHub calls are request/response only. The endpoint is usable with a
real pinned vMCP only when `TOOLHIVE_VMCP_ENDPOINT` is explicitly configured. The opt-in
`toolhub-gateway-contract` probe exercises the complete authenticated Go
gateway-to-vMCP path and requires explicit remote/stateful tool names plus a
pinned stateful image digest; unset configuration fails closed.

When `HUB_TOOLHUB_ENDPOINT` is set, runtime supervision injects the endpoint into
the copied Hermes config using only a `${HUB_TOOLHUB_TOKEN_ENV}` reference. With
`HUB_TOOLHUB_AUTOSTART=true`, supervision starts the shipped `hub-toolhub`
process against `HUB_TOOLHUB_STORE`; otherwise the endpoint is operator-managed.
The existing generated configuration remains unchanged when these variables are
unset. `toolhub-hermes-contract` verifies the real upstream Hermes process can
call the endpoint, survive a gateway-only restart and retain its session volume
when real vMCP inputs are supplied.

Remote MCP and stateful container MCP definitions remain immutable catalog
records. Their connection metadata may carry a non-secret private backend URL;
credential references remain opaque and contain only backend locator/key names.
The stage does not inject secret values or migrate the existing catalog.
ToolHive v0.48.0 is verified as an external local adapter contract only; its
sidecar images, resource budgets and provider credentials remain deployment
inputs, not repository dependencies.

## Workload classification and lifecycle

- `shared`: stateless and mount-free; per-request authorization is mandatory.
- `per-user`: context-owned state is persistent across process restart and is
  not deleted by stop/restart.
- `per-job`: temporary job state has an expiry and is cleaned after success,
  failure or cancellation.

The Go model validates these controls. CPU, memory, PID and egress enforcement
for container/remote workloads remains at the ToolHive/Docker controller. For
container MCP, `HUB_TOOLHIVE_ADMISSION_ENDPOINT` is the explicit private
controller contract: Go sends the immutable execution plan without identity or
credential values and requires a JSON receipt proving `running` state, enforcement,
digest-pinned images and the requested execution policy. An empty HTTP 200/204 is
not proof. If the endpoint is unset, rejects the plan, or returns a missing or
mismatched receipt, the call fails closed.

## Bounded CLI

CLI definitions name one allowlisted executable and fixed arguments. Tool input
arguments are structured, declared, scalar values; they are converted to argv
without shell parsing or interpolation. The runner uses an exact environment,
an allowlisted cwd under its root, finite output and timeout budgets, and kills
the process tree on cancellation. Host execution does not claim network or
filesystem isolation; those policies require an external sandbox and fail
closed when absent.

## Reconnect and rollout

Projection changes use transport reconnect only. Hermes home, sessions, memory,
run state and job state are not rebuilt or copied. Dynamic catalog migration,
list-change orchestration, ToolHive installation and full secrets
infrastructure remain later stages.
