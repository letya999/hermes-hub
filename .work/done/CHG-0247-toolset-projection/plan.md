# CHG-0247: M29.6 Project whole toolsets into core ≤20 and extended live discovery (M29 stage 6)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R6 -> this plan -> `state.yaml`.
Previous stage: CHG-0246. Next stage: CHG-0248. Evidence: state.yaml.

## Goal

Give Hermes a bounded core and live extended inventory while keeping per-call scope and effect checks identical.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and ADR-0089; docs/tools.md; docs/routines.md; M27 #383.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S6.1 Contract and failing regression (R6.1)

Anchors: internal/toolhub/gateway_projection.go projectedServer; internal/toolhub/gateway_call.go; internal/toolhub/tools_list.go; internal/stack/hermes_capability.go; internal/agenttools/capability.go.

Change:
1. Classify whole toolsets in catalog; compile via, include xor exclude, access read, scope and per-tool rules only where executor supports enforcement; reject unsupported native/raw MCP claims.

Tests (red first):
- `TestToolsetCoreLimitAndExtendedInvokeRevoke` — No split family, no >20 core, no silently added tool after catalog update, and extended invoke obeys the same fresh grant/scope as direct calls.

### S6.2 Implementation and consumer migration (R6.2)

Anchors: internal/toolhub/gateway_projection.go projectedServer; internal/toolhub/gateway_call.go; internal/toolhub/tools_list.go; internal/stack/hermes_capability.go; internal/agenttools/capability.go.

Change:
1. Select core by stable catalog priority with discovery pinned and ≤20 selected tools; move entire overflow toolset to extended and pin selection to catalog version.
2. Project only core direct schemas; use existing tools_list(query/name) and invoke for extended, rechecking live policy at dispatch; update tool descriptions, internal/stack/prompt_contract.go, docs/tools.md/routines.md and bump immutable hub-agent-tools version, then rebind/respawn.

Tests (red first):
- Extend `TestToolsetCoreLimitAndExtendedInvokeRevoke` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Read-only/tool-specific path policy is bypassed through invoke or raw MCP; admission refuses.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A6: No split family, no >20 core, no silently added tool after catalog update, and extended invoke obeys the same fresh grant/scope as direct calls.
- Live: Bind a profile with >20 selected tools, verify visible core count, discovery result and denied extended write.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No deferred schema hydration inside Hermes or ToolHive as an auth boundary.
- Dependencies: M29.1,M29.5.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
