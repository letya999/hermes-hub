# CHG-0251: M29.10 Integrate skills, connectors, raw MCP and capability packs with passports (M29 stage 10)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R10 -> this plan -> `state.yaml`.
Previous stage: CHG-0250. Next stage: CHG-0252. Evidence: state.yaml.

## Goal

Project existing skill and connector implementations into the new catalogs, tools and workspace without second authority paths.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and ADR-0085; SPEC-0084; M27 #383/#385/#387; docs/spaces.md.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S10.1 Contract and failing regression (R10.1)

Anchors: internal/skillstore; internal/stack/materialize.go; internal/toolhub/connector_facade.go; internal/toolhub/api/remote_mcp.go; internal/toolhub/api/recipe_catalogs.go; internal/agenttools/skills.go.

Change:
1. Map M24 effective skill catalog/mount, drafts/publish and M27 pack refs to cataloged toolsets and grants; keep the read-only /skills view and store write workflow.

Tests (red first):
- `TestSkillConnectorPassportProjectionAndRawMCPDenial` — One source of truth for visible skills/connectors; raw MCP remains denied by default; drafts and write effects require explicit operations.

### S10.2 Implementation and consumer migration (R10.2)

Anchors: internal/skillstore; internal/stack/materialize.go; internal/toolhub/connector_facade.go; internal/toolhub/api/remote_mcp.go; internal/toolhub/api/recipe_catalogs.go; internal/agenttools/skills.go.

Change:
1. Map connector discovery, prepare, enable, write-gating and raw MCP admission to scoped toolsets; ensure `via` is executable and effects are verified, not inferred from names.
2. Remove duplicate skill/connector projection switches after all callers move; ensure live list/search reflects enable, disable, revoke and pin changes.

Tests (red first):
- Extend `TestSkillConnectorPassportProjectionAndRawMCPDenial` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Unreviewed raw MCP or skill content cannot declare a new backend or grant.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A10: One source of truth for visible skills/connectors; raw MCP remains denied by default; drafts and write effects require explicit operations.
- Live: Enable one reviewed connector and draft one skill in dev, verify mount, discovery and revoke behavior.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No new pack acquisition engine, external provider API or skill marketplace.
- Dependencies: M29.1,M29.4,M29.5,M29.6.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
