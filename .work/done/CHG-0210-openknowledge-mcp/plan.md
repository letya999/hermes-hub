# CHG-0210: OK MCP wiring — read-only agent surface (M20 stage 9)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D8/D19 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R9 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/spike-m20.md` Q4.
Previous stage: CHG-0209. Next stage: CHG-0211.

## Goal

Wire the agent to OpenKnowledge's MCP server for a verified read-only
surface (read, search, links, history, lint-read, graph). The
`knowledge_ui_write` grant is deferred — OK has no per-path scoping
(spike), so agent writes stay on `memory_card` only.

## Read before coding

1. ADR-0086 D8; SPEC-0085 R9; spike-m20.md Q4 (no per-path scoping).
2. ToolHub capability/deny-wins model; `internal/agenttools` and the
   hub-agent-tools catalog; `internal/stack/mcp.go` MCP render.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- Tool-contract changeset in one commit (description, prompt-contract,
  `docs/tools.md`, `docs/routines.md`, catalog bump).
- `just check` before handoff.

## Slices

### S9.1 Verified tool list (R9.1)

Anchors: pinned OK version tool surface; `internal/stack` catalog.

Change:
1. Enumerate OK MCP tools at the pinned version; classify read vs
   write vs side-effect by test: admit `exec`, `search`, `links`,
   `history`, `audit`, `lint` (read mode), `config`, `palette`,
   `skills`; exclude `write`, `edit`, `delete`, `move`, `lint fix=true`,
   `preview_url`, `share_link`, `search ready=false`, `install`,
   `import`, `checkpoint`.

Tests (red first):
- `TestOKToolListVerified`, `TestOKSideChannelExcluded`.

### S9.2 `knowledge_ui` read grant (R9.1, R9.3)

Anchors: ToolHub capability sections; `internal/stack/mcp.go`.

Change:
1. Grant renders `http://open-knowledge:8080/mcp` with the read-only
   subset via capability profile — excluded verbs absent at the
   projection layer, not merely unadvertised.

Tests:
- `TestReadGrantSurfaceExact`, `TestWriteVerbNotProjected`.

### S9.3 `knowledge_ui_write` deferred (R9.2)

Anchors: capability model.

Change:
1. The write grant is not rendered anywhere; a write call — via surface
   or direct MCP — returns the standard deny/unavailable receipt;
   backlog issue documents the per-path-scoping precondition.

Tests:
- `TestWriteVerbDeniedDirect`, `TestWriteDenyReceipt`.

### S9.4 Revocation fail-closed (R9.2, R9.4)

Anchors: capability check path.

Change:
1. Grant revocation removes both the projected surface AND direct MCP
   call access; OK path args confined to the vault root.

Tests:
- `TestRevocationRemovesDirectAccess`, `TestOKPathConfined`,
  `TestOKDownDegradedHonest`.

## Red-team checklist

- A "read" verb with write side-effects caught by effect verification.
- Direct MCP call bypassing ToolHub → denied at capability check.
- OK path args (`../`, absolute) confined to vault root.

## Acceptance

A9.1–A9.6. Live checks recorded pending — requires live stack
(deferred — no-docker scope).

## Out of scope

- Agent write grant (deferred pending per-path scoping), OK
  theming/plugins, org instances.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
