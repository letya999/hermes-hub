## Essence

Prove the whole six-passport contract across real managed services, two users and CI rather than accepting mock-only success.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R12, A12).
- Stage plan: `.work/in-progress/CHG-0253-acceptance-ci/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S12.1 | R12.1 | `.github/workflows/ci.yml` | Add cross-package fixture/golden tests for atomic revisions, catalog parity, mount isolation, model credentials/quotas, grant revoke, core/extended tool paths and prompt injection; shard appropriately in CI. |
| S12.2 | R12.2 | `internal/runtime/model_relay_test.go` | Run `just check` and root coverage ≥85%, applicable `just security`, Broker check/unit/integration/e2e/race/fuzz/docs/release gates with ≥85% overall and per-package; run prebuilt/full Docker gates as appropriate. Perform real dev-stack end-to-end for two principals, org template update, model failover, connector/skill discovery, grant revoke, migration and respawn; record safe evidence separately from mocks and close CHGs/issues. |

## Acceptance criteria

- [ ] A12: All required gates and real dev evidence are green or transparently documented unavailable; no cross-user leakage or stale authorization.
- [ ] A real opt-in CLIProxy/provider session verifies the claimed provider integration; synthetic providers verify only Hub routing and cannot close that gate.
- [ ] Execute M29 dev acceptance matrix against pulled/rebuilt images and record safe receipts/endpoints without secrets.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestM29TwoUserPassportEndToEnd` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Concurrent revoke/rebind/migrate across two users and partial failure cannot leave mixed revision or leaked principal state.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Execute M29 dev acceptance matrix against pulled/rebuilt images and record safe receipts/endpoints without secrets.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- AGENTS.md; .github/workflows/ci.yml; .work/milestone-playbook.md; ADR-0092.

## Out of scope / dependencies

- No production rollout or account login without separate user instruction.
- Dependencies: M29.0-M29.11. Stage issues: #424, #425, #426, #427, #428, #429, #430, #431, #432, #433, #434, #435. 
