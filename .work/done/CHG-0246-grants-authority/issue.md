## Essence

Answer what this space may do in one compact allow map while preserving per-call authorization and revocation.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R5, A5).
- Stage plan: `.work/in-progress/CHG-0246-grants-authority/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S5.1 | R5.1 | `internal/toolhub/api/capability_policy.go CapabilityPolicy` | Define capability operation catalog with fixed own/cross-user semantics, issuer and parameters; map existing policies, governance grants and control operations without allowing template grants to mint authority. |
| S5.2 | R5.2 | `cmd/hubctl/grant.go` | Compile the current allow arrays only from trusted platform/org ceilings plus active ledger; expose why/issuer/revision through operator audit, keep snapshot read-only. Check current grant epoch on every direct/invoke/model/config operation; self-editing permitted choices cannot broaden ceiling and revocation is immediate. |

## Acceptance criteria

- [ ] A5: Absent operation denies; an explicit revoke blocks next call even with stale prompt/passport; own and other-user permissions remain distinct operations.
- [ ] Grant and revoke one benign operation in a two-user dev stack; confirm only the intended next call changes.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestGrantSnapshotRevokeBlocksDirectAndInvoke` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Agent writes grants.yaml, changes own tools.yaml, or sends another principal ID; no authority increases.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Grant and revoke one benign operation in a two-user dev stack; confirm only the intended next call changes.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/rbac.md; ADR-0092 D3; SPEC-0046; #397.

## Out of scope / dependencies

- No second human-versus-agent subject and no grants as editable template defaults.
- Dependencies: M29.1,M29.2. Stage issues: #425, #426. 
