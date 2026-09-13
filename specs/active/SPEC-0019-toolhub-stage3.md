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
not claim a sandbox that is not present.

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
