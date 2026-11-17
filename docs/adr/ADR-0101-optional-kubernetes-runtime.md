---
description: Optional Kubernetes execution backend, runtime ownership and persistent space isolation.
last_verified: 2026-11-17
---
# ADR-0101: Optional Kubernetes runtime substrate

Status: Accepted 2026-11-17. Implements the owner-scoped runtime slice of #58.

## Decision

Docker remains the default. A configured Kubernetes backend implements the same
supervisor compute boundary; admission, generation leases, administrative holds,
job correlation and runtime HTTP authorization stay with their existing owners.
The [required operator spike](../../.work/in-progress/CHG-0319-kubernetes-runtime/operator-eval.md)
rejects ToolHive Operator for runtime spawning, while leaving MCP connector
scheduling undecided. No new operator or CRD is introduced.

Use a separately selected namespace per environment, one labelled Pod per logical
principal/context/runtime mode, one retained PVC per space, generation-scoped
Secrets, and deny-by-default NetworkPolicy. Stable runtime IDs do not derive from
Pod names, UIDs, IPs or nodes. Every mutation checks owner/context/generation;
deletion also uses the observed immutable UID. Foreign objects are refused.
Suspend removes compute and ephemeral credentials, preserving the PVC and ledger.
Respawn creates a new generation with the same identity and space data.

Published configuration/skills and runtime environment are bounded private Secret
projections. Runtime data uses only the selected space's PVC; no hostPath, Docker
socket, privilege or service-account token enters a runtime. RuntimeClassName is
explicit configuration; empty omits it. CPU/memory requests and limits bound Pods.
Only reviewed control-plane peers and DNS are allowed by the baseline network
policy; external providers require an explicit egress path. Pod phases and standard
conditions project into the existing allowlisted status vocabulary, never raw
messages or environment data.

The supervisor uses namespace-scoped RBAC and the official Go Kubernetes client.
It stays the sole runtime reconciler and reuses existing bounded recovery/backoff.
Unknown ownership/API failures do not authorize deletion or replay. A running
runtime's readiness remains the authenticated Hermes contract, beyond Pod readiness.

## Consequences

Kubernetes is opt-in for deployments and requires a provisioned namespace, storage,
CA trust and enforcing CNI. Existing authored data is migrated deliberately into
the corresponding PVC layout; empty claims never silently import host homes.
PVC backup/snapshot and restore are operator responsibilities separate from control
plane state. Cross-space host mounts and Docker-only sidecars cannot silently fall
back to a host daemon. Connector cells, HA leadership and a measured multi-host
capacity decision are outside this owner-scoped slice.
