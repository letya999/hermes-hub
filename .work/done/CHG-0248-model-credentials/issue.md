## Essence

Allow model services to use rotatable API keys and pinned per-account CLIProxy sessions without exposing credential bytes.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R7, A7).
- Stage plan: `.work/in-progress/CHG-0248-model-credentials/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S7.1 | R7.1 | `services/credential-broker/provider/provider.go Provider` | Define reviewed contract types and provider-backed storage refs for model API keys and bounded OAuth session files, including owner/share scopes, revision, rotation and revoke; do not turn Broker into general canonical secret store. |
| S7.2 | R7.2 | `internal/runtime/model_relay.go` | Deliver short-lived least-privilege leases to the exact CLIProxy/relay workload, prove per-account pinning or isolate each account in its own proxy instance; prevent secrets in DTOs, argv, YAML, diagnostics and audits. Add Broker module contract/negative tests, integration and e2e gates; document migration from cliproxy/auths and current per-space key env with explicit operator action. |

## Acceptance criteria

- [ ] A7: Two separate session principals and one key can rotate/revoke independently; their bytes never enter agent views or persisted ledger fields.
- [ ] Before declaring CLIProxy integration ready, consume an operator-provisioned disposable session through the real pinned CLIProxy image; synthetic Broker tests alone are insufficient.
- [ ] Using synthetic credential fixtures in dev, rotate/revoke session A while B and API-key route continue; inspect redacted receipts.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestModelSessionLeaseOwnerRotationAndRevocation` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Cross-user lease, expired session, stale proxy and diagnostic path cannot reveal or reuse a session.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Using synthetic credential fixtures in dev, rotate/revoke session A while B and API-key route continue; inspect redacted receipts.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/services/credential-broker/index.md; ADR-0064/0087; docs/services/model-relay.md.

## Out of scope / dependencies

- No provider login, guessed OAuth API, browser-profile copy or remote MCP installer.
- Dependencies: M29.0,M29.1,M29.3. Stage issues: #424, #425, #427. 
