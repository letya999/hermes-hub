# CHG-0108: Full credential coverage — file/shared/http_header connectors

## Context

CHG-0107 shipped dynamic broker contracts limited to per-user env
collect-and-deliver. After merging feat/m13-closeout (generic remote OAuth,
scope:shared, file/json/brokered deliveries, MCPB) the remaining fail-closed
gaps:

- `file` credential delivery (container mount + env path) — synthesized
  contracts emit only env deliveries; non-env inputs bail.
- `shared` Sharable definitions — synthesis refused; shared flag is applied
  after binding so the contract lacks allow_shared.
- Remote defs via source path whose creds are all local-plane (http_header,
  brokered, oauth-minted) — `selfInstallUsable` demands a contract that can
  never exist.

## Design

1. Partition credential inputs: contract-coverable (env, "", file, json) vs
   local-plane (http_header → header on reviewed origin, brokered → cell
   cred-proxy, oauth → minted flow). Only coverable inputs enter contracts,
   required sets, or the "operator must add a contract" error.
2. Synthesis maps file inputs to contract `file` deliveries
   (Target=mount path, EnvName=env var carrying the path) and Sharable shared
   definitions to `allow_shared` contracts. Broker RegisterContract accepts
   allow_shared; routes/state/oauth_provider stay rejected (no ToolHub
   producer/consumer).
3. `shared` workload marking moves before bindReviewedContract so the
   synthesized contract is shared-capable from the start.

## Steps

- [ ] synthesis + binding changes with tests
- [ ] broker RegisterContract allow_shared + tests
- [ ] docs (ADR amendment, spec, api-contract)
- [ ] just check; rebuild; live: file-cred + shared + remote-header connector

## Wildberries live recovery, 2026-10-17

- [x] Submit the all-optional protected Broker form and confirm under the local owner.
- [x] Reproduce missing optional credentials.env in helper and fallback regression tests; fix and deploy workload-controller through dev-rebuild.
- [x] Review the pinned upstream's runtime hosts; publish a private local 0.0.4 network policy retaining the existing artifact and tool contract.
- [x] Enable and verify real category, product metadata and review reads through ToolHub MCP.
- [ ] Search and price-card reads: upstream HTTP 403; upstream DOM mode requires a dedicated CDP browser, absent from this deployment. Automatic execution policy rejected starting a separate headless Chrome; no browser was launched.
- [x] just check passed: own Go statement coverage 85.81%, format/vet/tidy/staticcheck/gosec/docs/actionlint/config checks passed. No dependency changes.
