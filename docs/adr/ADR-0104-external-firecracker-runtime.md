---
description: External Firecracker microVM backend via Uno-style control plane.
last_verified: 2026-11-17
---
# ADR-0104: External Firecracker runtime backend

## Status

Accepted (CHG-0322).

## Context

ADR-0103 added `containerRuntime=kata` as the third in-cluster isolation level.
Two cases remain: clusters without KVM-capable nodes at all (managed EKS), and
deployments that want the smallest possible VMM footprint plus zero host
filesystem surface inside the guest. A dedicated Firecracker control plane
(Uno-style, proven in the oracle project) covers both.

## Decision

`execution.backend=firecracker` selects an external Uno-compatible control
plane instead of Kubernetes. The supervisor keeps the same `ComputeBackend`
contract (`Spawn/Observe/Verify/Remove`); the box runs the same
`hub-runtime serve` contract (`/readyz`, `/v1/execute`) on a vsock-bridged
address. `kata-fc` as a Kubernetes RuntimeClass was rejected: it requires
devmapper thin provisioning and offers no isolation gain over Cloud
Hypervisor for this workload.

### Isolation and network model

- Guest has **no network device**. The only channel is virtio-vsock to the
  mini-Uno/ Uno host agent; egress is an application-level allowlist on the
  host side (guest-local relay aliases forward only to allowlisted relay
  endpoints). This is strictly stronger than a NetworkPolicy.
- State is a virtio-fs share of the space directory, jailed to that share —
  equivalent durability to the Kubernetes PVC contract; no tar checkpoint
  window. The tar/checkpoint protocol remains an optional hardening track.
- The guest receives only the per-generation `HUB_RUNTIME_AUTH` and relay
  URLs. Uno credentials, provider keys, and control-plane secrets never
  enter the guest (verified by fact at acceptance).

### Fail-closed semantics

Missing Uno endpoint/token, unreachable control plane, failed box, or an
ambiguous run submission never degrade to runc/docker/kata: they surface as
bounded supervisor failures. Ambiguous submissions replay by idempotency key;
box replacement is bounded by `HUB_UNO_MAX_BOX_REPLACEMENTS`.

## Consequences

- Relays stay outside the box (same boundary as Kubernetes mode).
- Local development qualifies the full contract via mini-Uno on WSL2 /dev/kvm;
  customer deployments point at a real Uno control plane on bare metal.
- New host requirements: KVM + firecracker binary + golden kernel/rootfs
  assets; documented in docs/operations.md (follow-up).
