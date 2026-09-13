---
status: active
title: Dynamic ToolHub foundation: definitions, scoped connections and effective bindings
---
# Dynamic ToolHub foundation

This specification freezes the M2 data contract without switching the current Hermes
runtime to ToolHub. The Go package `internal/toolhub` owns catalog validation,
owner-scoped connection metadata, credential references and effective-binding
authorization. It does not start a connector, resolve a secret value, call ToolHive,
aggregate MCP servers or expose a gateway endpoint.

## Records and ownership

| Record | Meaning | Mutable state | Owner / authority |
|---|---|---|---|
| `ToolDefinition` | Immutable versioned capability manifest | Never edited; a changed manifest gets a new `version` | Go catalog; not an authorization grant |
| `Connection` | Provider account/workspace metadata and current credential reference | Status and revision; ID survives credential rotation | `OwnerRef{type: principal|context}`; existing identity IDs are authoritative |
| `CredentialReference` | Opaque backend locator and symbolic key names | Active reference is replaced on rotation; old ref is revoked | Exactly one `connection_id`; contains no secret value |
| `ToolBinding` | Effective definition/connection/runtime/policy projection | Enabled/disabled/revoked and projection revision | Exact `principal_id`, `context_id`, `runtime_id`, `policy_version` |
| `WorkloadInstance` | Execution lifecycle identity for a projected workload | Starting/running/stopped/failed/expired | Shared has no owner; per-user/per-job is context-owned |

The package imports `internal/identity` and accepts only its opaque lowercase IDs. It
does not create a principal, membership or context registry. A principal-owned
connection matches the authenticated `principal_id`; a context-owned connection
matches the authenticated `context_id`. Organization membership is still resolved by
the existing scope/identity boundary.

## Definition contract

Every definition contains:

- `definition_id` plus immutable SemVer-like `version`;
- one transport: `remote-mcp`, `container-mcp` or `bounded-cli`;
- source details: HTTPS URL with required TLS, or a digest-pinned image, or a direct
  command without a shell;
- declared tool names and `read`/`write` effect;
- symbolic credential input names only;
- `shared`, `per-user` or `per-job` classification with rationale;
- finite timeout, output, CPU, memory and PID limits;
- explicit egress host allowlist, logical isolated mounts and a bounded health probe.

Definitions are decoded with unknown fields rejected. Inline values, mutable container
tags, insecure remote URLs, shell-like commands, `${...}` command arguments, host
mounts, duplicate names, missing bounds and invalid status/transport values fail
closed. A shared definition must be stateless, mount-free and mark every credential
input `per_request: true`. Stateful or static-credential definitions are per-user or
per-job.

Definition registration is append-only by `(definition_id, version)`. Registering the
same immutable value is idempotent; registering a different value under the same key
is a conflict.

## Binding and authorization

A binding records the exact definition version, optional connection and credential
revision, runtime identity, policy version, workload class and projection revision.
Its ID is deterministic over:

```text
principal_id, context_id, runtime_id, definition_id, definition_version,
connection_id, credential_ref
```

Projected tools use a deterministic `hub-<definition>-<tool>-<hash>` name. Workload
IDs are deterministic over definition, class, owner context and job ID. These names
are stable across restarts and do not use provider content or secret values.

Before every backend admission, `Resolve`/`Authorize` checks, in order:

1. the schema and authenticated identity envelope;
2. exact principal/context/runtime/policy equality with the current binding;
3. enabled binding, immutable definition and matching workload class;
4. connection existence, status, definition family, revision and exact owner;
5. current active credential reference, connection link, revision and required key
   names.

Unknown, disabled, revoked, cross-owner and stale records are denied. `Authorize`
holds the registry read fence through the admission callback: a concurrent disable,
revoke or rotation cannot complete before an already admitted call, while a later
call observes the new state. The callback receives metadata only; no secret value is
returned by this contract.

## Workload classes

| Class | Allowed use | Required isolation |
|---|---|---|
| `shared` | Stateless per-request authorization only | No mounts or durable state; credentials are selected per request |
| `per-user` | OAuth/provider sessions, browser state, writable connector state or user credentials | Context-owned runtime and `connection-state` mount where stateful |
| `per-job` | Ephemeral high-risk or batch CLI/container work | Context-owned runtime, job ID, expiry and explicit cleanup state |

The registry models workload instances but does not start or reap them. ToolHive group
membership, an MCP tool list or a workload label never substitutes for Go binding
authorization.

## Examples

The following examples are contract fixtures; they are not enabled connectors:

```yaml
# remote MCP, per-user
definition_id: google-work
version: 1.0.0
transport: remote-mcp
source: {url: https://api.example.com/mcp, tls_mode: required}
credentials: [{name: GOOGLE_TOKEN, required: true, per_request: false}]
workload: {class: per-user, stateful: false, rationale: OAuth account is user-owned}
execution: {timeout_seconds: 30, output_bytes: 1048576, cpu_millis: 500, memory_mib: 256, max_pids: 32, egress: [api.example.com]}
health: {kind: http, value: /health, timeout_seconds: 5}
```

```yaml
# stateful container MCP, per-user
transport: container-mcp
source:
  image: ghcr.io/example/stateful-mcp
  digest: sha256:<64 lowercase hex characters>
  command: /app/server
workload:
  {class: per-user, stateful: true, rationale: provider session is durable,
   toolhive_version: v0.48.0,
   sidecar_images: [ghcr.io/stacklok/toolhive/egress-proxy@sha256:<64 lowercase hex characters>]}
execution:
  timeout_seconds: 60
  output_bytes: 2097152
  cpu_millis: 1000
  memory_mib: 512
  max_pids: 64
  egress: [service.example.com]
  mounts: [{source: connection-state, target: /state, read_only: false}]
```

```yaml
# bounded CLI, per-job
transport: bounded-cli
source: {command: glab, args: [mr, list, --output, json]}
workload: {class: per-job, stateful: false, rationale: one bounded process per job}
execution: {timeout_seconds: 45, output_bytes: 524288, cpu_millis: 500, memory_mib: 256, max_pids: 32, egress: [gitlab.com]}
```

An enabled binding becomes unavailable when its connection is disabled, its current
credential reference is revoked/rotated, or the binding itself is disabled/revoked.
The old revision remains historical metadata and is denied; a new effective binding
must point at the new reference. No secret value appears in a definition, list,
binding, audit record or error.

For example, a disabled binding remains in the append-only snapshot with
`status: disabled` and is omitted from `tools/list`; a revoked binding has
`status: revoked`, increments `revision` and is denied by `tools/call` even when
the caller reuses a previously projected tool name.

## Storage and rollout boundary

The current implementation uses the repository's existing atomic, permissioned JSON
file pattern for a narrow ToolHub registry snapshot. Save/load is strict and stable;
future ciphertext storage can move behind the `backend`/`locator` reference without
changing ownership or authorization semantics. No database, Vault, Kubernetes,
ToolHive backend, CLI runner, reconnect path, OAuth UX or secret-injection system is
introduced by this specification.

Current `settings.yaml`, `scope.yaml`, `spaces/<id>/connections/` and generated MCP
configuration remain unchanged and remain the active runtime path. Migration may
materialize definitions and bindings only in a later explicit stage after round-trip,
authorization, isolation and real provider evidence.

## Verification

Regression tests cover remote/container/CLI validation, unknown fields, digest/TLS,
unsafe mounts, bounded execution, exact owner and policy, disabled/revoked bindings,
stale rotation, concurrent admission versus revocation, concurrent rotation, file
round-trip and per-user/per-job lifecycle metadata. They are control-plane tests and
do not claim ToolHive, Docker, Hermes or provider integration success.
