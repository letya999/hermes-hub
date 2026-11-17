---
description: Managed Kubernetes network attestation and separate relay Pod boundary.
last_verified: 2026-11-17
---
# ADR-0102: Managed Kubernetes network boundary

Status: Accepted 2026-11-17. Extends ADR-0101 before runtime preflight changes.

## Decision

Docker's managed network has no default route. A Kubernetes CNI normally supplies
one, so checking the route table cannot establish Kubernetes isolation. Keep the
Docker check unchanged and add a separate Kubernetes preflight profile.

The Kubernetes profile requires a controller-authored, read-only policy receipt at
a fixed path. It binds namespace, Pod name, generation and opaque context hash to
the applied NetworkPolicy UID and spec digest. Namespace/name come independently
from the Downward API. An environment flag alone cannot select a weaker check;
missing, writable, malformed or foreign receipts fail closed. The receipt proves
the controller's applied policy, not CNI implementation correctness. Deployment
requires an enforcing CNI, and acceptance must exercise actual denied connections.

Runtime and relays are separate Pods. Containers in one Pod share a network
namespace, so sidecars alone cannot enforce the managed egress boundary. The
runtime may reach only its own generation's narrow ToolHub/model/optional egress
relays and DNS; it cannot reach control-plane backends directly. Relay Pods may
reach the reviewed control-plane peers. Additional provider egress is an explicit
operator NetworkPolicy. No public egress is silently granted to runtimes.

The existing Go relay processes and published registry retain their authorization,
request bounds, provider policy and credential contracts. Relay credentials are
individual Secret references, absent from the runtime's env/file projection.
Relays mount only their model state on the same retained space PVC. Runtime and
relay Pods co-locate for ReadWriteOnce storage. Suspend removes both Pods and
generation Secrets while preserving the space claim.

## Consequences

Kubernetes adds one relay Pod per runtime generation; this is necessary for the
existing managed security boundary. No custom operator, CRD, privileged runtime,
network-administration capability or runtime service-account token is introduced.
Namespace administrators and the configured CNI remain trusted infrastructure.
