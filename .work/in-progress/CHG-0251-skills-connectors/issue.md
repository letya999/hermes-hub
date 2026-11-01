## Essence

Project existing skill and connector implementations into the new catalogs, tools and workspace without second authority paths.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R10, A10).
- Stage plan: `.work/in-progress/CHG-0251-skills-connectors/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S10.1 | R10.1 | `internal/skillstore` | Map M24 effective skill catalog/mount, drafts/publish and M27 pack refs to cataloged toolsets and grants; keep the read-only /skills view and store write workflow. |
| S10.2 | R10.2 | `internal/agenttools/skills.go` | Map connector discovery, prepare, enable, write-gating and raw MCP admission to scoped toolsets; ensure `via` is executable and effects are verified, not inferred from names. Remove duplicate skill/connector projection switches after all callers move; ensure live list/search reflects enable, disable, revoke and pin changes. |

## Acceptance criteria

- [ ] A10: One source of truth for visible skills/connectors; raw MCP remains denied by default; drafts and write effects require explicit operations.
- [ ] Enable one reviewed connector and draft one skill in dev, verify mount, discovery and revoke behavior.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestSkillConnectorPassportProjectionAndRawMCPDenial` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Unreviewed raw MCP or skill content cannot declare a new backend or grant.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Enable one reviewed connector and draft one skill in dev, verify mount, discovery and revoke behavior.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- ADR-0085; SPEC-0084; M27 #383/#385/#387; docs/spaces.md.

## Out of scope / dependencies

- No new pack acquisition engine, external provider API or skill marketplace.
- Dependencies: M29.1,M29.4,M29.5,M29.6. Stage issues: #425, #428, #429, #430. 
