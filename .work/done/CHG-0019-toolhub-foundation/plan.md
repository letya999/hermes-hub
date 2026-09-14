---
title: M2 Dynamic ToolHub foundation
status: done
---
# CHG-0019: Dynamic ToolHub foundation

## Goal

Complete the contract portion of issues #20 and #22 and the M2 portion of #47. Add
the smallest Go control-plane model for immutable tool definitions, workload classes,
connections, opaque credential references, effective bindings and workload instances.
Reuse `internal/identity` and the existing atomic file storage pattern. Do not switch
the Hermes runtime or add a second ownership registry.

## Plan

1. Freeze SPEC-0017 for definitions, versioning, deterministic names, shared/
   per-user/per-job rules and strict validation.
2. Implement `internal/toolhub` with append-only definitions, separate metadata
   records, exact-owner/current-revision resolution and an authorization admission
   fence for concurrent lifecycle changes.
3. Add control-plane regressions for remote MCP, stateful container MCP, bounded CLI,
   unsafe/mutable inputs, cross-owner access, stale policy/binding, disabled/revoked
   states, concurrent rotation/revocation, file round-trip and workload identity.
4. Update architecture, migration and validation documentation without changing
   accepted ADRs or the current runtime route.
5. Run `just check`; run `just security` only if dependencies change; run
   `just docker-check` when Docker is available. Record exact outcomes and unavailable
   gates in state and validation docs.

## Explicitly deferred

ToolHub gateway wiring, ToolHive/vMCP, CLI runner, MCP reconnect, provider OAuth, full
secret encryption/injection, Kubernetes, Vault and real provider/Hermes integration.
