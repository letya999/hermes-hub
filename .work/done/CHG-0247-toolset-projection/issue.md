## Essence

Give Hermes a bounded core and live extended inventory while keeping per-call scope and effect checks identical.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R6, A6).
- Stage plan: `.work/in-progress/CHG-0247-toolset-projection/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S6.1 | R6.1 | `internal/toolhub/gateway_projection.go projectedServer` | Classify whole toolsets in catalog; compile via, include xor exclude, access read, scope and per-tool rules only where executor supports enforcement; reject unsupported native/raw MCP claims. |
| S6.2 | R6.2 | `internal/agenttools/capability.go` | Select core by stable catalog priority with discovery pinned and ≤20 selected tools; move entire overflow toolset to extended and pin selection to catalog version. Project only core direct schemas; use existing tools_list(query/name) and invoke for extended, rechecking live policy at dispatch; update tool descriptions, internal/stack/prompt_contract.go, docs/tools.md/routines.md and bump immutable hub-agent-tools version, then rebind/respawn. |

## Acceptance criteria

- [ ] A6: No split family, no >20 core, no silently added tool after catalog update, and extended invoke obeys the same fresh grant/scope as direct calls.
- [ ] Bind a profile with >20 selected tools, verify visible core count, discovery result and denied extended write.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestToolsetCoreLimitAndExtendedInvokeRevoke` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Read-only/tool-specific path policy is bypassed through invoke or raw MCP; admission refuses.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Bind a profile with >20 selected tools, verify visible core count, discovery result and denied extended write.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- ADR-0089; docs/tools.md; docs/routines.md; M27 #383.

## Out of scope / dependencies

- No deferred schema hydration inside Hermes or ToolHive as an auth boundary.
- Dependencies: M29.1,M29.5. Stage issues: #425, #429. 
