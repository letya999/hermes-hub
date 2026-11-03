## Essence

Represent each credential principal as one model service with an ordered internal model list and service ladder.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R8, A8).
- Stage plan: `.work/in-progress/CHG-0249-model-routing/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S8.1 | R8.1 | `internal/stack/models.go LoadModelRegistry` | Replace provider/model merge and switch map with default route, ordered service ladder, per-service models/reasoning/auth refs/sharing and validated service topology. |
| S8.2 | R8.2 | `docs/services/model-relay.md` | Implement model-unavailable fallback within service; quota/transport fallback across services before response starts; check requested tools/context/format compatibility, prevent stream replay. Add durable per-principal limits in a single {of,amount,per,unit,clock} grammar for requests/tokens/spend with windows such as 5h, 1d and 1w, shared/personal accounting and reservation; enable provider_quota percent only on verified session-level usage/reset, otherwise reject configuration. |

## Acceptance criteria

- [ ] A8: Two isolated sessions then one key follow declared order; unavailable model stays in service; exhausted service advances; post-stream failure never retries.
- [ ] User-selected route changes only with `models.switch`; a shared subscription uses one shared counter and never leaks its credential to another principal.
- [ ] Provider-specific quota percentages remain unavailable until live per-session usage, limit and reset are proven with the pinned CLIProxy/provider path.
- [ ] Synthetic dev providers return model 404, 429, stream partial and quota telemetry; verify route selection and usage receipts.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestModelServiceLadderQuotaIsolationAndStreamFence` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Shared key budget race, spoofed auth index or missing quota telemetry cannot bypass limit.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Synthetic dev providers return model 404, 429, stream partial and quota telemetry; verify route selection and usage receipts.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- ADR-0073; SPEC-0070; docs/services/model-relay.md; M28 #402; #400.

## Out of scope / dependencies

- No guessed provider quota APIs or production account login; M28 owns shared relay tenancy.
- Dependencies: M29.2,M29.7,M29.5. Stage issues: #426, #431, #429. 
