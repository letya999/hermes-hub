---
description: Operator-selected runtime isolation, prerequisites and real boundaries.
last_verified: 2026-11-17
---
# Runtime sandbox selection

runc is the default. Stronger isolation is an operator deployment choice;
install and qualify the runtime on the actual execution host first. See
[ADR-0103](adr/ADR-0103-optional-sandbox-runtime.md) and the measured
[cell spike](../.work/in-progress/CHG-0320-sandbox-runtime/spike.md).

## Docker and Compose

Set `HUB_CONTAINER_RUNTIME=runc|runsc|kata` in the operator process that runs
`hubctl up` (or in Compose's operator interpolation environment). The rendered
Hermes service has `runtime: ${HUB_CONTAINER_RUNTIME:-runc}`; the supervisor gets
the same variable and passes an explicit `--runtime` on supervised runtime and
isolated script launches. Bounded CLI cells inherit that selection by default.
`HUB_CLI_RUNTIME=runc|runsc|kata` explicitly overrides it on the workload-controller,
which validates the choice at startup. This is
deployment configuration, not a tool grant or a retired settings.yaml override.

Isolated scripts keep 128 MiB/0.5 CPU, with 16 host PIDs on runc and 64 on runsc
to accommodate sandbox host threads. CLI cell limits remain policy-owned:
the controller never raises a requested PID/memory ceiling to make a sandbox fit.
Qualify those limits; the cell spike used 64 PIDs and 256 MiB.

The daemon must register the exact selected name. A missing/broken runtime
fails; Hermes never retries using runc. Sandbox runtime spawning currently
requires managed supervision without browser/meet. Those sidecars have not been
qualified under these sandboxes. Static/unmanaged deployments must provide
sandbox-compatible discovery themselves; the static Compose selector alone is
not integration acceptance.

For runsc, follow the [official install](https://gvisor.dev/docs/user_guide/install/)
and keep the complete release archive together, including gvisor-bin. Register
the daemon runtime with `--platform=systrap` for a no-KVM node. No host-network
flag is required. For Kata, install the complete guest/VMM/shim bundle and
register its shim using Docker's `runtimeType`; choose QEMU or Cloud Hypervisor
in **node configuration**, not in a space. Verify KVM access and a real VM boot.
An executable binary or successful kata-runtime check alone is insufficient.
The upstream [Docker guide](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-with-docker.md)
tests QEMU; Cloud Hypervisor needs deployment-specific acceptance.

Docker embedded DNS is unavailable to runsc's isolated network stack. For
supervised sandboxes, the supervisor discovers only `toolhub`, `model-relay`
and, when required, `egress-relay` aliases on that space's internal network and
adds their private IPv4 addresses to the runtime hosts file. Missing, invalid
or conflicting aliases refuse spawn. Public DNS, host gateway, Docker socket
and core control names are not added. If a relay is recreated with another IP,
suspend and respawn the runtime to refresh those pins. Cells already address
their egress proxy by private IP.

## Kubernetes

Set Helm `execution.backend: kubernetes` and
`execution.containerRuntime: runsc` (or `kata`). With no explicit override, the
supervisor uses the same-named RuntimeClass. Set
`execution.kubernetes.runtimeClassName` for an operator-installed class such as
`kata-qemu` or `kata-clh`; its handler, scheduling constraints and overhead must
match the selected sandbox and actual node capabilities. Hermes does not install
or attest runtime binaries, create cluster-wide RuntimeClasses, or grant its
supervisor cluster-admin access. A custom class pointing to runc is an operator
misconfiguration; the class name cannot prove kernel isolation.

An absent RuntimeClass fails Pod admission; an unavailable handler/VMM leaves
the Pod unready and fails the bounded spawn. Inspect namespace Pod events on the
node to distinguish these cases. The supervisor never removes the selected
class or creates a fallback Pod. Empty class with default runc keeps the cluster
default (the operator must ensure its handler is runc). Control-plane class is
configured separately at top-level runtimeClassName; relay Pods remain trusted
default-runtime components. A Pod is one sandbox boundary: do not colocate an
untrusted runtime with trusted relay credentials in the same Pod.

Known limitation: managed Kubernetes runtimes project private inputs through a
memory-backed EmptyDir populated by an init container and mounted via `subPath`.
Under Kata those writes live inside the guest VM, so the runtime container sees
an empty mount and exits fail-closed at startup (`passport activation
mismatch`). The identical spec passes under runsc. `containerRuntime=kata` in
Kubernetes therefore remains valid configuration — admission and fail-closed
behavior are verified — but managed Pods cannot reach readiness until the input
contract is reworked (disk-backed EmptyDir or projected volumes). This is a
qualified Hermes volume contract gap, not a Kata/VMM failure: both shipped VMMs
booted real guest kernels on the qualification node.

Both backend/runtime and class are saved in the supervisor ledger. To change
them: settle jobs, suspend runtimes, verify compute termination and clear owned
orphans, then restart the supervisor with the new selection. Active-state
changes refuse startup; warm adoption checks the actual runtime/class. PVCs,
spaces and user data survive this procedure.

## Qualification and limitations

Use a disposable dev daemon/namespace and synthetic spaces. Check real cellinit
canaries, readonly root/helper/config/skills, writable own workspace, cleanup,
resource bounds, allowed proxy egress and denied direct/foreign egress. Then
check real managed Hermes readiness/message transport and suspend/respawn. Record
source SHA, image digests, runtime/VMM/kernel versions and cold-start/memory
measurements. Missing runtimes/classes must fail in the same deployment.

For Kata, verify the exact KVM node/VMM. On nodes without KVM, only selection
and fail-closed logic are qualified; do not report VM execution success. Docker
MemUsage for Kata can omit VM and guest-kernel memory; measure host VMM/cgroup
usage before sizing. The current [threat model](threat-model.md#runtime-isolation-backends)
states what each backend actually protects.
