# CHG-0322: External Firecracker runtime backend (Uno-style)

## Goal

`execution.backend=firecracker` — fifth runtime execution mode: real microVMs via
an external Uno-compatible control plane. No Kubernetes involvement; the same
`ComputeBackend` contract (Spawn/Observe/Verify/Remove) is preserved.

## Architecture

- **Backend** (`internal/supervisor/firecracker.go`): implements ComputeBackend
  against the Uno API subset. Long-running `hub-runtime serve` inside the box;
  `Address` = host-forwarded port bridged to guest :8080 over vsock.
- **Uno client** (`internal/uno`): typed client, error taxonomy
  (ambiguous->idempotent replay / retryable / terminal), `?wait_s` long-poll.
- **mini-Uno** (`cmd/mini-uno`, `internal/firecracker`): local Uno-compatible
  control plane managing real Firecracker processes on /dev/kvm; vsock exec +
  TCP bridging; virtio-fs shares; application-level egress allowlist.
- **Guest agent** (`hub-runtime fc-agent`): vsock exec server (port 5000),
  vsock->TCP dial forwarder (5001), guest-local TCP listeners proxying to
  host relay endpoints via vsock CID2 (egress allowlist enforced host-side).
- **State**: virtio-fs share of the space dir = same durability model as the
  Kubernetes PVC path; no host-FS writes outside the jailed share.
- **Secrets**: box gets per-generation HUB_RUNTIME_AUTH + relay URLs only.
  Uno token, provider keys, control creds never enter the guest.

## Phases

1. F0: ADR-0104 + this plan (done).
2. F1: `internal/uno` client + unit tests vs HTTP stub.
3. F2: mini-Uno + fc-agent + backend; contract via tests/sim.
4. F3: golden image (kata kernel + docker-export rootfs), live WSL2 acceptance.

## Non-goals (MVP)

No clone-source boxes, no per-box backup schedules, no SSH access, no
sleep/wake fast paths beyond process lifecycle, no Oracle Spawner port.
