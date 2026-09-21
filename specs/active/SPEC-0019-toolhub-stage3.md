---
status: active
title: Dynamic ToolHub stage 3 catalog, projection and migration contract
---
# Dynamic ToolHub stage 3

This specification completes the M2 control-plane contract for catalog projection
and migration. ToolHub remains opt-in; the generated direct-MCP runtime remains the
default until a later rollout gate enables one stable MCP route.

## Manifest catalog and effective binding

`service_catalog` reads immutable `ToolDefinition` manifests when a ToolHub store is
explicitly configured. Each entry contains only definition/version, transport,
workload class, declared tool names, exact owner binding status and missing symbolic
credential names. It never reads `self-services.json` to infer enablement and never
returns secret values. `service_enable` and `service_disable` create or change the
exact `principal_id`, `context_id`, `runtime_id` and `policy_version` binding. A
foreign owner, revoked binding, missing current credential reference or stale policy
is denied before a backend call.

Definition identity is `(definition_id, version)` and registration is append-only.
Binding identity is deterministic over the authenticated scope, definition version,
connection and credential reference. Credential rotation keeps the connection ID,
revokes the old reference and requires a new effective binding revision. Disabled
bindings remain historical metadata and are omitted from projected tools.

## Projection and reconnect contract

Each principal/context/runtime projection has a persisted monotonic revision. Binding
enable, disable, connection status changes and credential rotation advance that
revision. `ReconnectController` exposes an observable callback that fires at most once
per revision and is quiet when unchanged; it is a transport hook only and does not
own Hermes sessions, copy homes or restart the runtime. `tools/list` and
`tools/call` resolve current state independently of cached MCP visibility, so revoke
or rotation denies before backend execution even when an old session remains open.

## Enforcement admission

Container execution requires an external controller admission receipt containing the
exact workload ID, `running` state, enforcement proof, image digest, sidecar image
digests and execution policy. An empty HTTP 200/204 is not proof. Missing, malformed,
stale or mismatched receipts fail closed. The Go gateway does not claim to enforce
CPU, memory, PID, mounts or egress itself; bounded CLI host execution likewise does
not claim a sandbox that is not present. The lightweight local generic adapter is
started with `hubctl connector generic-controller`; it accepts only a trusted
artifact plan, uses ToolHive environment secret references, allocates an owned
per-workload network and proxy volume, and returns a private endpoint only after
Docker inspection. Trusted registration requires a confirmed tool contract from a
real MCP `tools/list` or a review manifest checked against one; claimed import
tools are not enough. It refuses host state binds and shared credential-bearing
workloads. Stateful fallback plans may declare only controller-owned named
connection/job state volumes; credential-bearing fallback plans require an owner
workspace `credentials.env`, validated against the definition and passed to
Docker as an env-file (never to ToolHive arguments). Before starting MCP code it requires the ToolHive CLI to advertise
create-time controls for read-only root, no-new-privileges, dropped capabilities,
seccomp and bounded resources; a stock ToolHive build that lacks these flags is
denied rather than repaired with a post-start update. An explicit
`docker_fallback` opt-in may instead use Docker to create a bounded container
before starting the reviewed artifact, then use ToolHive only as the private
remote proxy and lifecycle API. Shared stateful or credential-bearing
definitions remain denied on this fallback.
The MCP companion stays on an internal network without the forwarding token; a
separate fixed-target relay authenticates and publishes only the loopback `/mcp`
endpoint because Docker Desktop cannot publish ports directly from an internal
network. The companion stdio client synthesizes JSON-RPC method-not-found for
`server/discover` so MCP servers that ignore that RPC still complete
`initialize`. The copied bridge must be a Linux ELF; Windows `hubctl.exe` is
rejected. Builder bootstrap policy `rootless-buildkit-bootstrap-v1` is distinct
from the MCP runtime profile and does not permit privileged or unconfined
workers. Capacity proof for 95 distinct credential files plus five uses of one
shared file is sequential unique Docker networks/volumes/containers when the
host cannot allocate 100 concurrent internal networks. A hundred users are a
hundred registered bindings; live processes stay bounded by `max_active` and
idle cleanup. Identical artifact SHAs reuse one image; they do not share
processes, volumes or credentials. `tools/call` after revoke is denied before
the backend.

## Explicit migration

`hubctl migrate-toolhub` supports dry-run (default) and `--apply`. It imports only
explicit `tools.include` entries from existing settings, preserves enabled/disabled
state, records owner-scoped connection metadata and symbolic credential references,
and copies no secret values. Discovery-only generated MCP and legacy self-service
entries are reported as unmatched until a manifest is supplied. Apply writes the
existing atomic JSON store after validation, keeps a `.rollback` copy and restores it
when the write fails. Source settings, scopes, generated MCP, homes and legacy state
are not deleted. A migration report is safe to retain in the change evidence.

## Verification boundary

Regression tests cover exact owner isolation, disabled/revoked bindings, current-state
authorization, projection quietness, concurrent rotation/revocation, secret-free
admission, catalog enable/disable and migration dry-run/rollback. These tests are
control-plane evidence. Real ToolHive/vMCP calls, Linux resource/egress measurements,
stateful persistence and real-Hermes reconnect require approved external fixtures and
remain separate acceptance gates. HTTP reachability, discovery or a mock backend is
not integration success.
