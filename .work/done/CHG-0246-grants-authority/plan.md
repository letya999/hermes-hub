# CHG-0246: M29.5 Make grants.yaml a simple effective operations view over trusted ceilings and ledger (M29 stage 5)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R5 -> this plan -> `state.yaml`.
Previous stage: CHG-0245. Next stage: CHG-0247. Evidence: state.yaml.

## Goal

Answer what this space may do in one compact allow map while preserving per-call authorization and revocation.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/rbac.md; ADR-0092 D3; SPEC-0046; #397.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S5.1 Contract and failing regression (R5.1)

Anchors: internal/toolhub/api/capability_policy.go CapabilityPolicy; internal/toolhub/api/governance.go Governance; internal/toolhub/capability_store.go; internal/toolhub/gateway_call.go; cmd/hubctl/grant.go.

Change:
1. Define capability operation catalog with fixed own/cross-user semantics, issuer and parameters; map existing policies, governance grants and control operations without allowing template grants to mint authority.

Tests (red first):
- `TestGrantSnapshotRevokeBlocksDirectAndInvoke` — Absent operation denies; an explicit revoke blocks next call even with stale prompt/passport; own and other-user permissions remain distinct operations.

### S5.2 Implementation and consumer migration (R5.2)

Anchors: internal/toolhub/api/capability_policy.go CapabilityPolicy; internal/toolhub/api/governance.go Governance; internal/toolhub/capability_store.go; internal/toolhub/gateway_call.go; cmd/hubctl/grant.go.

Change:
1. Compile the current allow arrays only from trusted platform/org ceilings plus active ledger; expose why/issuer/revision through operator audit, keep snapshot read-only.
2. Check current grant epoch on every direct/invoke/model/config operation; self-editing permitted choices cannot broaden ceiling and revocation is immediate.

Tests (red first):
- Extend `TestGrantSnapshotRevokeBlocksDirectAndInvoke` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Agent writes grants.yaml, changes own tools.yaml, or sends another principal ID; no authority increases.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A5: Absent operation denies; an explicit revoke blocks next call even with stale prompt/passport; own and other-user permissions remain distinct operations.
- Live: Grant and revoke one benign operation in a two-user dev stack; confirm only the intended next call changes.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No second human-versus-agent subject and no grants as editable template defaults.
- Dependencies: M29.1,M29.2.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
