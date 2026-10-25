# CHG-0167: Per-user subdomains — DNS/TLS addressing design pass (M18, issue #146)

Chain: `docs/adr/ADR-0078-per-user-dns-tls.md` -> `specs/active/SPEC-0076-*` (after ADR acceptance)
-> this plan -> `state.yaml`. Evidence: `research.md`.
Consumes: ADR-0077/SPEC-0075 (#145). Blocks: #147 hosting layer.

## Goal

Decide and record the addressing layer: provider-neutral DNS record
contract over the four mandated profiles, per-user wildcard TLS via
hub-side DNS-01 delivered to sish, Broker-held credentials, fail-closed
cross-user naming.

## Read before coding

1. AGENTS.md, docs/adr/ADR-0077 (D4/D6), specs/active/SPEC-0075,
   internal/exposure/{config,sish,enrol}.go (zone label, key_ref, SSH
   channel), internal/credentialbroker/client.go (`NewForBinding`),
   internal/sshcap/ (exec/files for VPS delivery).
2. Issues #146 (this), #145 (upstream), #147 (downstream consumer).

## Preconditions and rules

- Design pass only in this CHG: research, ADR, SPEC, impl-CHG list.
- No implementation until ADR-0078 is accepted and SPEC is frozen.
- DNS credentials never leave the Broker pattern; no provider SDK guess —
  clients are minimal HTTPS+JSON per documented APIs only.

## Slices

### S1 Provider + TLS research — done

`research.md`: four-profile matrix, REG.API limits/allowlist, TTL and
propagation semantics, TLS fork (wildcard vs per-host, challenge types),
executor placement, cross-user chain.

### S2 ADR-0078 — done (Proposed)

Provider-neutral contract, four declarative profiles, delegated
`hub.<domain>` reference path, per-user wildcard certs hub-issued via
DNS-01 and pushed to sish, regru static-IP precondition fail-closed.

### S3 SPEC-0076 draft — next

Freeze: record contract (`Ensure`/`Remove`/`WaitAuthoritative`, typed
errors, effective-TTL reporting), profile schema (provider kind, zone,
credential ref, quirks), zone↔space binding record, naming rules and
reserved labels, ACME account/cert storage, renewal driver, doctor /
preflight diagnostics for the regru IP-allowlist precondition, audit kind
for DNS/cert operations.

### S4 Implementation CHGs — pending SPEC

Planned first slices: `internal/dns` contract + regru/DO/GCP clients;
hubctl onboarding (`dns setup`, delegation verify, per-user zone records);
ACME issuer + SSH cert delivery + renewal; doctor checks; wiring into
`exposure`/stack render so `zone`+`relay.domain` come from the bound
profile; live acceptance per profile on the real accounts.

## Red-team checklist

- Cross-user: Alice asks for `x.bob.hub.<domain>` — config layer denies
  (zone bound to her space), contract layer refuses names outside
  `*.zone.hub.<domain>` for her binding.
- regru on dynamic IP must fail closed in doctor with the two exits named.
- A DNS credential must never appear in argv/env of a user workload,
  logs, tool results or ledger DTOs.
- A failed DNS-01/renewal must not tear down live tunnels or records;
  stale cert keeps serving until expiry, alert before lapse.
- Zone confinement: delegated profiles must be unable to write outside
  `hub.<domain>` even if called with a crafted name (verify).

## Acceptance

ADR-0078 Proposed → owner decision; then per-SPEC acceptance IDs.

## Out of scope

Registrar operations (registration, transfer, whole-domain NS moves),
site hosting (#147), inbound SSH (#148), raw TCP exposure, HTTP-01 /
hub-terminated TLS, per-endpoint A records, managed-provider profiles
(cloudflared carries its own name path from #145).
