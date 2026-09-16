# Trusted artifacts and local ToolHive runtime

Closeout to leave this stage: [closeout.md](closeout.md).

## Owner-directed implementation, 2026-09-16

Owner rejected patching ToolHive and requested a lightweight maintainable MVP.
Owner also rejected requiring administrator-prepared images. Implement automatic
GitHub import for Python, Node, Go and Rust, preserving the full security gates
below. Generate recipes from conventional manifests; ask for literal entrypoint
selection when ambiguous, not an administrator-built image. Reuse existing Go
catalog/admission code. Remove only our rejected Compose experiment. Do not patch
ToolHive, introduce a large Docker gateway, or weaken pre-start requirements.
Existing provider deployment and unrelated working-tree changes remain unchanged.
Owner authorized a distinct minimal trusted-builder bootstrap profile for the MVP;
MCP runtime constraints remain unchanged. The accepted direction does not permit
privileged mode, Docker socket/host mounts/network or an unconfined final profile.

Validated runtime experiment: reuse `internal/companion` for private stdio-to-HTTP,
with stock ToolHive remote workload mode in the same bounded Docker container.
ToolHive requires runtime discovery even for remote URLs. A tiny private Unix
bootstrap shim answers ping and empty container listing, denying all mutations;
it has no connection to the actual Docker daemon. Real synthetic stdio discovery,
call, RunConfig export, stop and remove passed under create-time resource limits.
This is integration-test evidence only: production owner authorization, private
state, output limits, egress policy and lifecycle/budget remain to implement.
A real same-UID sibling probe subsequently read ToolHive's synthetic forwarding
secret via /proc. Do not adopt the one-container candidate: isolate MCP/bridge
and stock ToolHive control in separate bounded containers with distinct state.
The tiny deny-all bootstrap remains useful inside the proxy-only container.
Two-container fixture subsequently passed with network `none` on MCP and proxy
sharing only that exact owned network namespace. Both containers are inspected
before start, including private PID mode and distinct volumes. MCP tools/call
checks control-state invisibility and absence of readable ToolHive secret markers.
This remains a synthetic integration fixture, not production controller wiring.
Do not use native companion on the host for untrusted upstream MCPs.

## Full objective

Implement issues #97, #98 and #73 without modifying upstream Hermes or the
existing opt-in Telegram deployment.

1. Pin Git sources; export a bounded, secret-free context. Support explicit,
   reviewed Dockerfile/Python/Node/Go/Rust recipes, never arbitrary host shell.
2. Build with a dedicated docker-container BuildKit builder; retain OCI digest,
   provenance and SBOM. Preflight exact tools/effects/env and immutable manifest.
3. Start only with pre-start resource/security/network enforcement. Use pinned
   ToolHive RunConfig/proxy/lifecycle, exact-owner admission and named volumes.
4. Bound active workloads with queue/idle cleanup; test 100 synthetic bindings,
   including repeated credential references without shared processes or state.
5. Run real Python/Node stdio integration and escape/leak checks; run just check,
   security and docker-check. Mock evidence is not integration evidence.

Current investigation: ToolHive v0.48.0 CLI and RunConfig expose no CPU/memory/PID
settings. Do not use post-start docker update as generic enforcement; investigate
the validated remote-workload bootstrap rather than a Docker API gateway.
