---
description: Frozen acceptance contract for the M18 outbound exposure primitive (named public endpoints over tunnel workloads).
last_verified: 2026-10-25
---
# SPEC-0075: Outbound exposure primitive

Frozen: 2026-10-25. Decision: ADR-0077. Changes: CHG-0165 (design);
implementation CHGs follow.

- R1 (#145): An exposure is a named, owner-scoped record mapping an endpoint
  name to a declared internal target (a user-space service port or the
  restricted public-listener surface). Records live in reviewed connection
  config; hub control and gateway routes are never valid targets, and
  arbitrary public hostnames, ports or targets fail closed.
- R2: Each exposure runs as a per-binding tunnel workload admitted through
  the connector admission path — owned network, pinned egress, proven
  receipt (digest/limits/caps/seccomp/net/volume inspect), finalizer
  removal to verified zero. No hub-resident public proxy and no shared
  multi-user tunnel: run credentials are per-binding.
- R3: All credentials go through the Broker. Run credentials (sish SSH
  private key, cloudflared run token) deliver into the workload env-file;
  provider admin/API credentials (e.g. Cloudflare account token) are
  operator-scoped and never enter user workloads. No secret in argv, tool
  results, logs or ledger DTOs.
- R4: The agent surface is name-only: `exposure_open`, `exposure_close`,
  `exposure_list`, `exposure_status` act on declared endpoint names.
  Cross-user name claims fail closed — by hub-enforced user-scoped
  namespacing on sish, by account-side name attachment on cloudflared.
- R5: The sish profile (reference) generates a per-user ed25519 keypair at
  binding; pubkey enrolment on the operator VPS is a documented one-time
  operator step (hubctl prints the material to enrol). The workload runs a
  stock ssh client `-R <name>:<remote-port>:<target>:<port>` through a
  credential-bound TCP forward; requested names are validated against the
  binding's user-scoped namespace before the session opens.
- R6: The cloudflared profile (alternate) runs one remotely-managed tunnel
  per exposure: run token in the workload env, `--protocol http2` forced
  through the CONNECT proxy, hostname attach and DNS route by a CF-API
  control path under an operator credential — taken only with explicit
  operator acceptance of edge plaintext.
- R7: Bounds: an opt-in space feature flag, per-owner cap on concurrent
  exposures, idle-timeout and lifetime-ceiling reap that kills the
  workload and emits a close receipt; `exposure` audit kind records every
  open/close/failure. First slices are HTTP(S)-only; raw TCP exposure is
  out.
- R8: A provider profile is declarative — provider kind + run-credential
  kind + name-allocation rule; new providers join the contract without
  forking the admission or tool paths.

Acceptance A1..A8: each requirement has regression tests through its real
Go boundary, updated docs, and the full local gate. Live acceptance —
a real sish VPS and, for the alternate profile, a real Cloudflare account —
is separate evidence and cannot be asserted from fixtures; ADR-0054's
deferred items rerun over the deployed tunnel.
