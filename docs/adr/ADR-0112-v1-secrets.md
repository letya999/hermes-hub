---
description: v1 secrets - credential-broker is the only secret owner; runtimes and workers hold credential refs; leased values are injected at the boundary by cred-proxy or egress-gateway; no secret in env, argv, DTOs, rows, logs or tool results.
last_verified: 2026-11-20
---
# ADR-0112: v1 secrets

Status: proposed (M40.1 #599, CHG-0334). Direction agreed with the owner on
2026-11-20; record text awaits owner review.
Delivery: M40.13 (#611); model provider keys as refs in M40.12 (#610).
Builds on: ADR-0016 and ADR-0049 (credentials never in the model plane),
ADR-0022 (credential-broker module). Keeps the existing credential refs,
broker leases and cred-proxy.

## Context

v0 has three secret holders (credential-broker, `internal/credstore`,
ToolHub OAuth state) and secret-bearing files outside the broker such as
`model-grants.json`. Runtimes reach providers through relays that live in
the untrusted runtime container.

## Decisions

1. **One owner.** credential-broker is the only owner of secrets and OAuth
   state. Every secret-bearing file outside it is moved or deleted.
2. **Refs, not values.** Runtimes, workers, contracts and rows hold
   credential refs only. No secret in env, argv, DTOs, rows, logs, audit or
   tool results.
3. **Boundary injection.** The broker leases a value to cred-proxy (inside
   a worker) or egress-gateway, which injects it into the outbound request
   for an allowed domain. The runtime never sees it.
4. **egress-gateway is the only exit** when `trusted=false`: domain
   allowlist from the effective spec, metadata and private address ranges
   blocked, run token required.
5. **No new machinery.** No token exchange, per-space CA, TLS interception
   or placeholder rewriting in v1; the existing refs and leases are enough.

## Enforcement

`TestNoSecretInRuntimeEnvArgvOrFiles`, `TestMetadataCIDRBlocked`,
`TestEgressRequiresRunToken` (M40.13); `TestNoProviderKeyInRuntime`
(M40.12); backend conformance "no secret in env/argv" (M40.3); the broker
module's own 85% coverage and contract tests; gitleaks in CI.

## Alternatives

- **Sentinel credentials rewritten by a TLS-intercepting proxy.** Rejected
  for v1: more trust machinery than the existing boundary injection needs.
- **Secrets in each service's own store.** Rejected: it multiplies secret
  holders, the v0 problem.

## Preserved v0 invariants

- Credentials never enter the model plane.
- The broker's hard rules (AGENTS.md): no secret in ledger DTOs, tool
  results, argv or examples; TLS, Origin/CSRF, audit and authorization are
  never disabled for compatibility.
