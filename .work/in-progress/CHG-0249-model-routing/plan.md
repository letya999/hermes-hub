# CHG-0249: M29.8 Route by model service, enforce quotas, and fail over safely (M29 stage 8)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R8 -> this plan -> `state.yaml`.
Previous stage: CHG-0248. Next stage: CHG-0250. Evidence: state.yaml.

## Goal

Represent each credential principal as one model service with an ordered internal model list and service ladder.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and ADR-0073; SPEC-0070; docs/services/model-relay.md; M28 #402; #400.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S8.1 Contract and failing regression (R8.1)

Anchors: internal/stack/models.go LoadModelRegistry; internal/runtime/model_relay.go modelRelayRegistryHandler/ladderFor/modelCall; internal/runtime/model_relay_quota.go fetchQuotaLines; docs/services/model-relay.md.

Change:
1. Replace provider/model merge and switch map with default route, ordered service ladder, per-service models/reasoning/auth refs/sharing and validated service topology.

Tests (red first):
- `TestModelServiceLadderQuotaIsolationAndStreamFence` — Two isolated sessions then one key follow declared order; unavailable model stays in service; exhausted service advances; post-stream failure never retries.

### S8.2 Implementation and consumer migration (R8.2)

Anchors: internal/stack/models.go LoadModelRegistry; internal/runtime/model_relay.go modelRelayRegistryHandler/ladderFor/modelCall; internal/runtime/model_relay_quota.go fetchQuotaLines; docs/services/model-relay.md.

Change:
1. Implement model-unavailable fallback within service; quota/transport fallback across services before response starts; check requested tools/context/format compatibility, prevent stream replay.
2. Add durable per-principal limits in a single {of,amount,per,unit,clock} grammar for requests/tokens/spend with windows such as 5h, 1d and 1w, shared/personal accounting and reservation; enable provider_quota percent only on verified session-level usage/reset, otherwise reject configuration.

Tests (red first):
- Extend `TestModelServiceLadderQuotaIsolationAndStreamFence` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Shared key budget race, spoofed auth index or missing quota telemetry cannot bypass limit.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A8: Two isolated sessions then one key follow declared order; unavailable model stays in service; exhausted service advances; post-stream failure never retries.
- Live: Synthetic dev providers return model 404, 429, stream partial and quota telemetry; verify route selection and usage receipts.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No guessed provider quota APIs or production account login; M28 owns shared relay tenancy.
- Dependencies: M29.2,M29.7,M29.5.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
