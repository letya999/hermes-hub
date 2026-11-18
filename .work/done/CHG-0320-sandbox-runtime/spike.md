---
description: Cell compatibility measurements before sandbox implementation.
last_verified: 2026-11-17
---
# Decision memo — #394 → #202

Adopt optional runsc/systrap for managed Docker spaces/cells; keep runc default.
Expose Kata as an operator choice requiring qualification of the exact VMM/node.
Cloud Hypervisor has a working cell PoC here. QEMU is deferred: guest-agent vsock
readiness timed out on this nested node. Never fall back to runc.

## Fixture and measured results

Source before implementation: ff793b3c. Isolated Docker-in-Docker 29.10.0 in the
existing dev Docker, Linux 6.6.87.2 WSL2. Dind image digest
`sha256:4bbc7223f81db790097c6dea42fb93e05b8fead4f0431fbe6ccaba6e8473201c`.
Only synthetic fixtures and a dedicated daemon volume, no personal spaces or
outer Docker socket. Privileged dind is installation infrastructure, never the
workload security profile. KVM API 12 and CREATE_VM succeeded; vhost devices exist.

gVisor release-20261109.0, explicit systrap without KVM use. Official archive
SHA512 `1a6b8f74c37b4931ef473f67ec1f601690855bedc52ac709f2fac965c69677aa0426ef44598d8eee1aed72cfdb9df87a2a5f352cb3348880fc61a7732bc10f2a`.
Kata 4.3.1, commit 03cf2ec06a5d302f23af811744f6bcea49988bfb, official Go static
archive SHA256 `66b35b06f6b41b56f8606ddf3ed3e5660b0971d1e309d903457c4056a0b9eff0`.
Cloud Hypervisor booted guest Linux 6.18.56; upstream Docker support is tested
with QEMU only, so this CLH result is a local PoC.

Five sequential warm-image cells each: real cellinit, actual repository seccomp
profile, uid 10001, cap-drop ALL, no-new-privileges, readonly root/helper/skills,
rw /work, noexec /tmp, 1 CPU, 256 MiB, 64 pids. CLI image content ID
`sha256:69b401dd9912b6df272f9fddb275b052987d203f57765da832920a40cedae34e`.
Create+start and two canary exec calls are timed, including Docker CLI overhead.

| Runtime | Create+start median (range), ms | Canary median, ms | Docker MemUsage |
|---|---:|---:|---|
| runc | 649 (597–991) | 279 | 9.6–10.4 MiB |
| runsc/systrap | 574 (415–1767) | 258 | 21.7–23.0 MiB |
| Kata/Cloud Hypervisor | 5110 (2161–19504) | 604 | 4.1–4.7 MiB, guest process only |

Kata MemUsage excludes VM/VMM/guest-kernel memory; its shipped configuration
requests 2 GiB guest RAM. These are startup observations, not throughput, p95,
total-memory or capacity benchmarks. Extraction-contaminated earlier runs were
excluded; final samples ran after installation completed.

All 15 cells started. Canary/check/clean/procs, readonly mount deny checks,
workspace writes, absent /state/socket/broker material, denied-domain CONNECT
(403) and blocked direct public egress passed. Squid config came from the real
api.GenericProxyConfig; proxy used private IP, as production cells do. Allowed
HTTPS CONNECT passed 5/5 runc, 5/5 runsc, 4/5 Kata: one Kata request hit the
fixture's five-second external-network timeout. Complete Kata network acceptance
is therefore not claimed. No real accounts/provider mutations were attempted.

QEMU 11.0.1/KVM failed guest-agent vsock readiness, also after enlarging dind's
/dev/shm from 64 MiB to 4 GiB. KATA_CONF_FILE pointing at the CLH config was
rejected by the upstream shipped-config allowlist; selecting its shipped default
symlink enabled the CLH PoC. Do not bypass that guard in Hermes. VMM selection
belongs to node configuration. Raw synthetic artifacts and Go probe are ignored
under .work/tmp/sandbox-spike; actual deployments are not committed.

Update during validation: on the kind node (containerd, nested KVM) the same
Kata install booted both shipped VMMs — QEMU and CLH Pods reached `Running`
with guest kernel `6.18.56` against the node's `6.6.87` WSL2 kernel, so the
DinD vsock failure was fixture-specific and QEMU itself is viable. The managed
Kubernetes path then surfaced a real Kata incompatibility in Hermes' own volume
contract, recorded in validation.md.

## Compatibility and boundary

Docker embedded DNS (127.0.0.11) failed inside runsc; identical IP proxy requests
passed. Pin only the space's private relay aliases at supervised spawn and refuse
missing/ambiguous aliases. Keep the internal network; never repair DNS with host
networking. Static/unmanaged layouts need operator-supplied sandbox-compatible
discovery. Managed spaces use the supervisor. Browser/meet remain unqualified.

Kubernetes adds one backend constraint: the supervisor projects runtime inputs
through a `Medium: Memory` EmptyDir written by an init container and consumed via
`subPath`. Under Kata those writes live inside the guest, so the runtime
container sees an empty mount and fails closed at startup. The identical spec
works under runsc. Choosing Kata in Kubernetes therefore needs the input
contract reworked (disk-backed EmptyDir or projected volumes) before admission
can be expected to succeed; until then `container_runtime=kata` in Kubernetes
fails closed as required — admitted only where the class exists, never silently
downgraded.

Seccomp restricts host syscalls for runc; it is not a guest kernel. Runsc adds
userspace syscall mediation; Kata adds a VM/guest kernel. All still trust the
host administrator, runtime/VMM, exposed storage, network enforcement and Hub
authorization. A runtime/class name is configuration evidence, not attestation.

Primary sources: [gVisor installation](https://gvisor.dev/docs/user_guide/install/),
[platforms](https://gvisor.dev/docs/user_guide/platforms/),
[Docker DNS](https://gvisor.dev/docs/user_guide/faq/),
[security](https://gvisor.dev/docs/architecture_guide/security/),
[Kata installation](https://github.com/kata-containers/kata-containers/blob/main/docs/installation.md),
[Kata Docker guide](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-with-docker.md).
