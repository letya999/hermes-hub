---
description: "Credential Broker remains a separate process and security boundary inside the monorepo."
last_verified: "2026-09-18"
---
# ADR-0022: Credential Broker boundary

## Decision

Keep `services/credential-broker` as a separately licensed, separately built Go
module and process. The monorepo provides its source, image binary, workspace
membership, local gates and CI coverage, but does not flatten its imports or
silently replace ToolHub's current credential path.

ToolHub, Communication Hub and the runtime each receive a purpose-specific
adapter and signing audience before Broker enrollment is enabled. ToolHub keeps
source review, ToolHive admission, binding projection and reconnect ownership.
Credential values and runtime materialization never enter Hermes messages, MCP
tool results or job/audit records.

## Why

The Broker supplies reviewed credential contracts, approval pairing, provider
references, grants, leases and materialization. These concerns are useful for
M5.3, but an in-process package is not a security boundary and the existing
ToolHub model has different connection/reference lifecycles. Keeping the
process boundary allows an opt-in migration and a clean rollback to the current
encrypted store.

## Consequences

- The root image contains `credential-broker` and `hub-credential-broker`, but
  no service, keys, providers or contracts are enabled automatically.
- Root CI runs the Broker's own Linux gates and image build alongside Hermes.
- A future integration must add reviewed contract mapping plus approve, event,
  grant, lease, revoke and same-path runtime tests before replacing the current
  form/store path.
- The copied subtree retains its MIT license; Hermes remains AGPL-3.0-only.
