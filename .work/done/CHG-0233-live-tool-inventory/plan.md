# CHG-0233: Live caller-scoped tool inventory (tools_list + invoke)

Chain: `docs/adr/ADR-0089-live-tool-inventory.md` D1-D5 -> `specs/active/SPEC-0023-toolhub-control-plane.md`
(Control MCP section) -> this plan -> `state.yaml`.

## Goal

The model can ask, inside a live session, "what tools do I actually have right
now" and receive a compact, honest, caller-scoped answer — including connector
and MCP tools installed or enabled during this session or a neighbouring one —
without prompt rebuild, MCP reconnect or runtime respawn, and with bounded
token cost. A listed tool absent from the client's cached `tools/list` stays
callable through the existing `invoke` dispatch.

## Read before coding

1. `internal/toolhub/gateway_projection.go` (`projectedServer`, `system_guide`)
2. `internal/toolhub/control_http.go` (`addControlTools`, `invoke` registration)
3. `internal/toolhub/grants.go` (`AllowedControlOperations`, `RequireControlOperation`)
4. `internal/toolhub/projection.go` (`ListProjectedTools`)
5. `internal/toolhub/governance_store.go` (`filterProjectedTools`)
6. `internal/toolhub/control_mcp_test.go` (fixture + session helpers)
7. `internal/stack/prompt_contract.go` (system_guide guidance line)
8. `docs/adr/ADR-0024-toolhive-local-vmcp.md`, `docs/tools.md`, `docs/rbac.md`,
   `specs/active/SPEC-0023-toolhub-control-plane.md`

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work (tree carries unrelated M23/M24 edits).
- Red test first, then fix. Go stdlib only.
- `invoke` must keep routing through `g.call`/`AuthorizeProjectedCall` — it is a
  presentation fallback, never an authorization widening.
- `just check` before handoff.

## Slices

### S1.1 `tools_list` gateway tool

Anchors: `gateway_projection.go` `projectedServer`; `control_http.go` `addControlTools`.

Change:
1. Register `tools_list` unconditionally beside `system_guide` in
   `projectedServer` (visible to every authenticated caller, like the guide —
   it only ever reports the caller's own admitted surface).
2. Handler re-reads live state per call: `Store.ListProjectedTools(auth)` +
   `filterProjectedTools` (identical inputs to `tools/list`) plus
   `allowedControls(auth)`.
3. Default output: compact `{name, definition, effect}` entries, sorted, capped
   by `limit` (default 100, max 200), plus `controls` and a usage note pointing
   at `invoke`/`skills_list`. Optional `query` (substring over name/definition/
   description, maxLength 256) or exact `name` returns matched entries with
   `description` and `input_schema`.

Tests (red first):
- `TestToolsListLiveInventoryAndIsolation`: alice's enabled binding appears in
  her `tools_list` and not in bob's; a binding enabled after the session's
  `tools/list` still appears on the next `tools_list` call.
- `TestToolsListQueryAndNameFilters`: query narrows results and attaches
  description+input_schema; `name` returns the single full spec.
- `TestToolsListOutputBounded`: limit honoured; no locator/secret material.

### S1.2 `invoke` under managed capability profiles

Anchors: `grants.go` `AllowedControlOperations`.

Change:
1. Under a managed profile, admit `invoke` as part of the connectors bundle
   (`connectors` = `personal` / `personal+team`). `connectors: ""` keeps the
   strict `control_operations` allowlist; `none` stays denied; the legacy
   grant path is unchanged.

Tests (red first):
- `TestManagedProfileAllowsInvoke`: `AllowedControlOperations` for a managed
  envelope yields `invoke`; `connectors: none` does not; `invoke` executes a
  managed projected tool through `g.call` (audit admit path).

### S1.3 Prompt-contract guidance + docs

Anchors: `internal/stack/prompt_contract.go` system_guide line; `docs/tools.md`
control-plane section; `specs/active/SPEC-0023-toolhub-control-plane.md`.

Change:
1. One sentence in the contract: `tools_list` is the live admitted inventory —
   call it before claiming a capability is missing; names it reports but the
   current tool list lacks are callable through `invoke`.
2. `docs/tools.md`: document `system_guide`, `tools_list`, `invoke` beside the
   control-operations table.
3. `SPEC-0023`: append the discovery-surface contract paragraph.

## Red-team checklist

- `tools_list` must never leak another principal's bindings, locators or
  credential names — same inputs as `tools/list` only.
- `invoke` on a non-admitted or foreign tool name must deny at
  `AuthorizeProjectedCall`, not at the wrapper.
- A revoked grant removes the tool from `tools_list` on the next call without
  reconnect.
- Response size stays bounded (`limit` cap; descriptions/schemas only on
  filtered results).

## Acceptance

- Unit tests above green; `just check` passes; coverage floor respected.
- Manual: enable a connector in one session; `tools_list` in another session
  shows it; `invoke` calls it by exact name.

## Out of scope

- Deferred schema hydration / native tools/list mutation mid-session (needs
  loop ownership — upstream Hermes).
- Catalog-change announcements via continuation jobs (post-MVP follow-up).
- BM25/indexed search; the `query` substring filter covers MVP scale.
- `hub:tools` (`mcp__hub__*`) inventory — different MCP server; the response
  note names that boundary.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated; index rows added; state done; folder moved to `.work/done/`.
