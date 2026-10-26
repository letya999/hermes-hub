# CHG-0168: Per-user DNS/TLS implementation (M18, issue #146)

Chain: ADR-0080 (accepted) -> SPEC-0078 (frozen) -> this plan -> `state.yaml`.

## Goal

Land the addressing layer: `internal/dns` provider-neutral record
contract with regru/digitalocean/gcloud clients, hub-side ACME DNS-01
issuer pushing per-user wildcard certs to the sish relay, hubctl
operator surface, doctor preflight, audit.

## Slices

### S1 `internal/dns` contract + config
Record/Provider types, declarative profile config (provider kind, zone,
credential ref, quirks), typed errors, authoritative-NS propagation check
(stdlib resolver against the zone NS), effective-TTL reporting.

### S2 Provider clients
Minimal HTTPS+JSON clients: regru (REG.API v2 zone functions), DO
(`/v2/domains/{zone}/records`), GCP (SA JWT → oauth2 token → Cloud DNS
changes). Shared contract tests over `httptest` fakes.

### S3 ACME issuer + delivery
`x/crypto/acme` order/renew with DNS-01 through the contract; account key
+ cert storage operator-side; SSH push of crt/key to the sish cert dir
(sshcap machinery).

### S4 hubctl + doctor + wiring
`hubctl dns` operator commands (setup/verify/delegate-check/records),
doctor check incl. regru egress-IP precondition, zone↔space binding into
exposure render path, `dns` audit events.

## Acceptance

SPEC-0078 A1..A8 — real Go boundaries, shared contract tests, docs,
local gate. Live acceptance per profile is separate evidence.

## Out of scope

Registrar operations, #147 hosting, inbound SSH, per-endpoint records,
hub-terminated TLS.
