# CHG-0253: M29.12 Multi-user live acceptance, CI gates, and milestone closeout (M29 stage 12)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R12 -> this plan -> `state.yaml`.
Previous stage: CHG-0252. Next stage: none. Evidence: state.yaml.

## Goal

Prove the whole six-passport contract across real managed services, two users and CI rather than accepting mock-only success.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and AGENTS.md; .github/workflows/ci.yml; .work/milestone-playbook.md; ADR-0092.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S12.1 Contract and failing regression (R12.1)

Anchors: .github/workflows/ci.yml; justfile; services/credential-broker/justfile; internal/stack/*_test.go; internal/toolhub/*_test.go; internal/runtime/model_relay_test.go.

Change:
1. Add cross-package fixture/golden tests for atomic revisions, catalog parity, mount isolation, model credentials/quotas, grant revoke, core/extended tool paths and prompt injection; shard appropriately in CI.

Tests (red first):
- `TestM29TwoUserPassportEndToEnd` — All required gates and real dev evidence are green or transparently documented unavailable; no cross-user leakage or stale authorization.

### S12.2 Implementation and consumer migration (R12.2)

Anchors: .github/workflows/ci.yml; justfile; services/credential-broker/justfile; internal/stack/*_test.go; internal/toolhub/*_test.go; internal/runtime/model_relay_test.go.

Change:
1. Run `just check` and root coverage ≥85%, applicable `just security`, Broker check/unit/integration/e2e/race/fuzz/docs/release gates with ≥85% overall and per-package; run prebuilt/full Docker gates as appropriate.
2. Perform real dev-stack end-to-end for two principals, org template update, model failover, connector/skill discovery, grant revoke, migration and respawn; record safe evidence separately from mocks and close CHGs/issues.

Tests (red first):
- Extend `TestM29TwoUserPassportEndToEnd` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Concurrent revoke/rebind/migrate across two users and partial failure cannot leave mixed revision or leaked principal state.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A12: All required gates and real dev evidence are green or transparently documented unavailable; no cross-user leakage or stale authorization.
- Live: Execute M29 dev acceptance matrix against pulled/rebuilt images and record safe receipts/endpoints without secrets.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No production rollout or account login without separate user instruction.
- Dependencies: M29.0-M29.11.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
