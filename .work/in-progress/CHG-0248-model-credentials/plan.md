# CHG-0248: M29.7 Broker model API keys and OAuth session material through scoped contracts (M29 stage 7)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R7 -> this plan -> `state.yaml`.
Previous stage: CHG-0247. Next stage: CHG-0249. Evidence: state.yaml.

## Goal

Allow model services to use rotatable API keys and pinned per-account CLIProxy sessions without exposing credential bytes.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/services/credential-broker/index.md; ADR-0064/0087; docs/services/model-relay.md.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S7.1 Contract and failing regression (R7.1)

Anchors: services/credential-broker/provider/provider.go Provider; services/credential-broker/materialize/materialize.go Manager.Prepare; services/credential-broker/httpapi/server.go Server.api; internal/stack/compose.go Compose; internal/runtime/model_relay.go.

Change:
1. Define reviewed contract types and provider-backed storage refs for model API keys and bounded OAuth session files, including owner/share scopes, revision, rotation and revoke; do not turn Broker into general canonical secret store.

Tests (red first):
- `TestModelSessionLeaseOwnerRotationAndRevocation` — Two separate session principals and one key can rotate/revoke independently; their bytes never enter agent views or persisted ledger fields.

### S7.2 Implementation and consumer migration (R7.2)

Anchors: services/credential-broker/provider/provider.go Provider; services/credential-broker/materialize/materialize.go Manager.Prepare; services/credential-broker/httpapi/server.go Server.api; internal/stack/compose.go Compose; internal/runtime/model_relay.go.

Change:
1. Deliver short-lived least-privilege leases to the exact CLIProxy/relay workload, prove per-account pinning or isolate each account in its own proxy instance; prevent secrets in DTOs, argv, YAML, diagnostics and audits.
2. Add Broker module contract/negative tests, integration and e2e gates; document migration from cliproxy/auths and current per-space key env with explicit operator action.

Tests (red first):
- Extend `TestModelSessionLeaseOwnerRotationAndRevocation` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Cross-user lease, expired session, stale proxy and diagnostic path cannot reveal or reuse a session.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A7: Two separate session principals and one key can rotate/revoke independently; their bytes never enter agent views or persisted ledger fields.
- Live: Using synthetic credential fixtures in dev, rotate/revoke session A while B and API-key route continue; inspect redacted receipts.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No provider login, guessed OAuth API, browser-profile copy or remote MCP installer.
- Dependencies: M29.0,M29.1,M29.3.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
