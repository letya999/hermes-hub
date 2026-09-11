---
description: Go ToolHub is the authorization boundary; ToolHive is execution infrastructure.
last_verified: 2026-09-11
---
# ADR-0013: Keep ToolHub authorization in Go

Accepted 2026-09-11.

## Decision

Hermes uses one stable authenticated Go ToolHub endpoint per runtime. Go derives the
principal, context and runtime from trusted transport state and revalidates the current
binding, policy and exact-owner connection/credential on every list and call. Authority
fields supplied by the model are ignored or rejected.

ToolHive Runtime is the conditional execution backend. Local vMCP is an internal,
replaceable aggregator for routing, conflict naming and filtering. Neither is the
canonical identity, policy, binding or credential-ownership store. A projection change
revokes calls immediately in Go and reconnects the MCP transport because ToolHive
v0.48.0 did not update an already-open session or emit `tools/list_changed` in spike
#11.

## Consequences

The project writes only the narrow security gateway and adapter, not a second MCP
aggregator. Managed secrets bypass Hermes and vMCP. Stateful or credential-bearing
connectors default to per-user/per-job isolation. The Go/Docker controller owns resource
limits; ToolHive owns workload execution and egress enforcement. Every workload and
sidecar is digest-pinned before production use.

Bearer authentication is sufficient for the private deployment. Future OIDC changes
the authenticated identity source, not per-call authorization semantics. Full RBAC,
Kubernetes, Vault and public/group ingress remain deferred and disabled.

## Evidence

Issue #11 records the ToolHive v0.48.0 compatibility spike and its conditional GO.
[The threat model](../threat-model.md) maps each abuse case to an enforcement point,
fail-closed behavior, audit evidence and regression owner.

Related: ADR-0009, ADR-0011, issues #20-#26, #46-#48 and #51.
