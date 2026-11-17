# ToolHive Operator spike — #58

Decision: **reject for Hermes runtime spawning**, 2026-11-17. This is separate
from CHG-0035's local vMCP endpoint evaluation. Connector scheduling remains a
separate decision; this result does not reject the operator for MCP workloads.

Before backend implementation, fetched and rendered the public operator and CRD
Helm charts at **0.66.0**. Operator OCI artifact digest:
`sha256:4841bbaf6bfe1456d9a4a223abccbf13ee3e50b9ff17d69541fef7d86712e4ce`;
CRDs: `sha256:8bf04267ab99874689e16f9376b2d2c205d7ce49387a3e5eb1e554bb38e4171e`.
The operator chart archive SHA256 is
`71d65cfba1c73560b6dd9046b813e3f50c0079c41c88304b0d83d5a6543e66ea`.
Commands: `helm pull ... --version 0.66.0`, `helm template spike <archive>`;
artifacts are ignored under `.work/tmp/k8s-spike/`.

| Criterion | Observation | Decision |
|---|---|---|
| Protocol | MCPServer transport is stdio, SSE or Streamable HTTP; Hermes exposes authenticated runtime HTTP, including execute/control/health | A protocol proxy does not implement the runtime contract |
| Lifecycle authority | MCPServer has separate proxy/backend replica controls; the hub already owns generation leases, holds and uncertain-run recovery | Avoid competing reconcilers for the same runtime |
| Storage/security | Pod templates, resource limits and Secret references are available | Useful capabilities, but plain Kubernetes resources already supply these |
| Operational footprint | Render creates operator Deployment plus RBAC; requests 10m CPU/64Mi, limits 500m/128Mi, plus CRDs | Extra baseline for no runtime-specific benefit; these are declarations, not measured usage |

Sources: [upstream guide](https://docs.stacklok.com/toolhive/guides-k8s/),
[pinned MCPServer API](https://github.com/stacklok/toolhive/blob/v0.66.0/cmd/thv-operator/api/v1beta1/mcpserver_types.go),
[pinned chart](https://github.com/stacklok/toolhive/tree/v0.66.0/deploy/charts/operator).

This spike is a chart/API compatibility evaluation. The operator was not installed
and no claim about its throughput, failure recovery or live MCP behavior is made.
The decisive mismatch is protocol/lifecycle ownership. Proceed with the existing
supervisor as reconciler over native Pod/PVC/Secret/NetworkPolicy resources, without
a custom operator or CRD. The owner's explicit second-substrate request establishes
the implementation scope; multi-host capacity demand has not been measured.
