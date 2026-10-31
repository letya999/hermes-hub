# CHG-0177: Outbound exposure primitive — design pass (M18, issue #145)

Chain: `docs/adr/ADR-0079-outbound-exposure.md` -> `specs/active/SPEC-0077-*` (after ADR acceptance)
-> this plan -> `state.yaml`. Evidence: `research.md`.
Blocks: #146 (TLS fork), #147 (addressing layer), ADR-0054 deferred live acceptance.

## Goal

Decide and record the exposure primitive: public URL → local port via
outbound-only per-binding tunnel workloads, with the managed-vs-self-hosted
provider fork settled on owner criteria (sish reference, cloudflared
alternate).

## Read before coding

1. AGENTS.md, docs/architecture.md (trust boundary, egress), docs/adr/ADR-0054
   (public surface), docs/prepared-connectors.md (admission/tcp_forwards),
   internal/sshcap/tunnel.go (idle/lifetime pattern).
2. Issues #145 (this), #146 (depends on D6), #147, #148.

## Preconditions and rules

- Design pass only in this CHG: research, ADR, SPEC, impl-CHG list.
- No implementation until ADR-0079 is accepted and SPEC is frozen.

## Slices

### S1 Provider research — done

`research.md`: candidates, per-user name isolation, egress fit, decision,
open questions.

### S2 ADR-0079 — done

Accepted 2026-10-25 on owner criteria (reliability/autonomy/security/cost):
sish reference provider, cloudflared alternate.

### S3 SPEC-0077 draft — next

Freeze: exposure record schema (name/target/provider), admission profile for
tunnel workloads, `exposure_*` tool contract, bounds (concurrency/idle/
lifetime), audit kind, fail-closed name rules, sish namespacing mechanism
(per-user zone vs verified custom domain), cloudflared profile
(API-token scopes, `--protocol http2`).

### S4 Implementation CHGs — pending SPEC

Planned first slices: connection-config `exposures` schema; per-user keypair
+ VPS enrolment flow; tunnel workload admission + Broker env-file delivery;
`exposure_open/close/list/status` tools + audit; sish provider profile
end-to-end; live acceptance (rerun ADR-0054 deferred items over a real
tunnel).

## Red-team checklist

- sish workload must not claim names outside its user's namespace — a
  stolen or confused client requesting another user's subdomain fails
  closed (D4 namespacing; verify in live acceptance).
- cloudflared profile: a workload holding a run token must not mint or
  steal hostnames — names are account-side.
- Exposure open against a control/gateway route must fail closed (D5).
- Idle/lifetime reap must kill the workload and emit a close receipt.

## Acceptance

ADR-0079 Proposed → owner decision; then per-SPEC acceptance IDs.

## Out of scope

DNS/TLS automation (#146), site hosting (#147), inbound SSH server (#148),
TCP/Spectrum-class exposure, quick tunnels, ngrok/zrok/bore profiles.
