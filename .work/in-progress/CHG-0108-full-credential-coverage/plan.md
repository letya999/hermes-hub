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

## GitLab incident additions (onboard-facc0e96)

`missing credential broker delivery for GITLAB_PROJECT_ID` — a placeholder in
`docker/.env.example` made an undeclared upstream env a required credential and
killed prepare before the form. Follow-ups requested by the owner:

4. Broker request-scoped effective contract: `extra_fields`/`extra_deliveries`
   on request create (optional only) and `opt:<id>` demotion of
   reviewed-required fields at form submit. Pinned contract never mutates;
   effective requiredness rides on request/credential and drives materialize.
   Spec `credential-broker-effective-contract`, ADR-0010.
5. Forms show required + optional fields with labels; user can demote any
   required field for that attempt (formnovalidate path).
6. ToolHub converts discovered extras into broker request extras and demotes
   undeliverable reviewed-required fields in the generic bind path, so prepare
   reaches the credential form on malformed/broad upstream metadata.
7. Source provenance: `ConnectionField.Source` (per parser file + `catalog`),
   conflict warnings on contradictory requiredness, `prepare_source`
   `credential_source` argument (fresh prepare and `request_key` replay) and
   `credential_sources` in status — user can pick `server.json` over a noisy
   `.env.example`.
8. One evolving status message per onboarding: later prepare notices edit the
   first delivered Telegram message (`EditMessageID` + outbox anchor lookup).

## Steps

- [x] synthesis + binding changes with tests
- [x] broker RegisterContract allow_shared + tests
- [x] broker extras/demote effective contract + form + ToolHub wiring
- [x] source provenance + credential_source selection + status edit-in-place
- [x] docs (ADR amendment, spec, api-contract; ADR-0010 + effective spec)
- [x] just check (covergo 86.28%/all pkgs ≥75; lint/vet/staticcheck/gosec/docs/config OK; broker module in Linux container: all tests green, 90.46% coverage, all pkgs ≥85)
- [ ] live: file-cred + shared + remote-header + gitlab retry

## Wildberries live recovery, 2026-10-17

- [x] Submit the all-optional protected Broker form and confirm under the local owner.
- [x] Reproduce missing optional credentials.env in helper and fallback regression tests; fix and deploy workload-controller through dev-rebuild.
- [x] Review the pinned upstream's runtime hosts; publish a private local 0.0.4 network policy retaining the existing artifact and tool contract.
- [x] Enable and verify real category, product metadata and review reads through ToolHub MCP.
- [ ] Search and price-card reads: upstream HTTP 403; upstream DOM mode requires a dedicated CDP browser, absent from this deployment. Automatic execution policy rejected starting a separate headless Chrome; no browser was launched.
- [x] just check passed: own Go statement coverage 85.81%, format/vet/tidy/staticcheck/gosec/docs/actionlint/config checks passed. No dependency changes.
