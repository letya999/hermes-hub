# CHG-0165: Outbound exposure primitive — design pass (M18, issue #145)

Chain: `docs/adr/ADR-0077-outbound-exposure.md` -> `specs/active/SPEC-0075-*` (after ADR acceptance)
-> this plan -> `state.yaml`. Evidence: `research.md`.
Blocks: #146 (TLS fork), #147 (addressing layer), ADR-0054 deferred live acceptance.

## Goal

Decide and record the exposure primitive: public URL → local port via
outbound-only per-binding tunnel workloads, with the managed-vs-self-hosted
provider fork settled (cloudflared reference, sish defined second).

## Read before coding

1. AGENTS.md, docs/architecture.md (trust boundary, egress), docs/adr/ADR-0054
   (public surface), docs/prepared-connectors.md (admission/tcp_forwards),
   internal/sshcap/tunnel.go (idle/lifetime pattern).
2. Issues #145 (this), #146 (depends on D6), #147, #148.

## Preconditions and rules

- Design pass only in this CHG: research, ADR, SPEC, impl-CHG list.
- No implementation until ADR-0077 is accepted and SPEC is frozen.

## Slices

### S1 Provider research — done

`research.md`: candidates, per-user name isolation, egress fit, decision,
open questions.

### S2 ADR-0077 — done

Proposed 2026-10-25; awaits owner review of the provider fork (D4, D6).

### S3 SPEC-0075 draft — pending ADR acceptance

Freeze: exposure record schema (name/target/provider), admission profile for
tunnel workloads, `exposure_*` tool contract, bounds (concurrency/idle/
lifetime), audit kind, fail-closed name rules, cloudflared profile
(API-token scopes, `--protocol http2`), sish namespacing requirement.

### S4 Implementation CHGs — pending SPEC

Planned first slices: connection-config `exposures` schema; tunnel workload
admission + Broker env-file delivery; `exposure_open/close/list/status`
tools + audit; cloudflared provider profile end-to-end; live acceptance
(rerun ADR-0054 deferred items over a real tunnel).

## Red-team checklist

- Workload holding a run token must not mint or steal hostnames (cloudflared:
  names are account-side; verify in live acceptance).
- sish path without hub namespacing allows cross-user subdomain claims —
  never ship the sish profile without D4's namespacing.
- Exposure open against a control/gateway route must fail closed (D5).
- Idle/lifetime reap must kill the workload and emit a close receipt.

## Acceptance

ADR-0077 Proposed → owner decision; then per-SPEC acceptance IDs.

## Out of scope

DNS/TLS automation (#146), site hosting (#147), inbound SSH server (#148),
TCP/Spectrum-class exposure, quick tunnels, ngrok/zrok/bore profiles.
