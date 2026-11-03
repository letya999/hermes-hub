# CHG-0260: tools.yaml toolset language and live projection (M30 stage 6)

Chain: ADR-0093 D5, D7 -> SPEC-0092 R6 -> this plan -> `state.yaml`.
Evidence: `../CHG-0254-passport-gap-audit/research.md` Findings 1, 4; CHG-0253 ten-space log.
Previous stage: CHG-0259. Next stage: CHG-0261.

## Goal

`tools.yaml` decides which tools the agent sees in its current session:
toolsets in `core` (direct schemas, ≤20 tools, discovery pinned) and
`extended` (via `tools_list`/`invoke`), each with `via`, include/exclude,
`access`, `scope` and per-tool `rules`. Applying a change updates the open
session (`tools/list_changed`) without restart. ToolHub records are derived
from the revision — never hand-built as in the M29 acceptance.

## Read before coding

1. SPEC-0092 normative `tools.yaml`, effect rows for tools.
2. `platform/catalogs/3/toolsets.yaml`; `internal/catalog` `Toolset`.
3. `internal/passport/passport.go` tools projection (`:309-390`).
4. `internal/toolhub/capability_store.go` `managedToolsLocked`, `managedSelectionLocked`, `CoreToolNames`; `api/capability_policy.go` `CapabilityProfile`, `CapabilityDecision`; `gateway_projection.go` `projectedServer`, `refreshProjection`.
5. `cmd/hubctl/capability.go` (manual path being replaced for managed spaces); `internal/stack/hermes_capability.go` `disabledToolsets`.

## Preconditions and rules

- Agent-tool contract rule from AGENTS.md (docs/tools.md, prompt contract, version bump, rebind, respawn).
- A restriction the executor cannot enforce is rejected, never ignored.

## Slices

### S6.1 Toolset catalog with bare ids (R6.1)

Anchors: `internal/catalog/catalog.go` toolset registry; `cmd/cataloggen`; `platform/catalogs/4/toolsets.yaml`.

Change:
1. Ids are bare (`discovery`, `files`, `documents`, `skills`, `ssh`, `connectors`, `watchers`, `browser`, `terminal`, `routines`, …); old `hub:*`/`native:*` ids become `aliases` for migration only.
2. Each toolset: `via` options with executor contract (`toolhub` supports include/exclude/access/scope.paths/hosts + rules; `mcp:<server>` supports access + scope.urls only where the server wrapper enforces it; `native` supports nothing but on/off), members with `effect` and `requires` (CHG-0256), `scope` kinds.
3. `discovery` = system_guide, tools_list, invoke; always core.

Tests (red first): `TestToolsetCatalogExecutorContracts`, `TestDiscoveryPinnedCore`.

### S6.2 Compile the language (R6.1)

NOTE (updated by CHG-0255 S1.0): core.*/extended.* keyset vocab + normative emission landed in CHG-0255 with member/via/budget checks; this slice keeps the scope-subset and executor-enforcement rules.

Anchors: `passport.go` tools projection; new `internal/passport/tools.go`.

Change:
1. Source: user proposes `core` and `extended` maps; compiler validates via per executor contract, include xor exclude, members exist, `access: read` drops write-effect members, `scope` kinds allowed, `rules.<tool>.scope ⊆ toolset scope`, scope paths ⊆ workspace roots (CHG-0259).
2. Placement: core in catalog order until the 20-tool budget; an overflowing toolset moves whole to `extended`; explicit `extended` stays extended.
3. Emit only `core` and `extended` maps (no `sections`, `catalog`, tool-name lists).

Tests (red first):
- `TestToolsPassportNormativeShape`
- `TestOverflowMovesWholeToolset`
- `TestUnenforceableRestrictionRejected` (`native` with scope; `mcp:playwright` urls without wrapper)
- `TestRuleScopeMustNarrowToolsetScope`

### S6.3 Derive ToolHub records at apply (R6.2)

Anchors: new `internal/toolhub/derive.go` `DeriveManaged(eff, catalog, definition) (Policy, Profile, Binding)`; `cmd/hubctl/space_apply.go` live hook; `capability_store.go`.

Change:
1. Policy: members = identity.user; ceiling = platform ∩ org ceilings' tool actions; Profile: selections = admitted members of core+extended pinned to the *bound* `hub-agent-tools` version and its digest; allows = actions whose `requires` ⊆ grants allow, with scope prefixes from `scope`/`rules`; `CoreTools` = core members; envelope fields from the runtime token (environment, generation, policy_version).
2. Binding: `store.Enable` for the principal envelope if absent (same as `capability --kind agent-tools`).
3. Apply publishes all three under one confirmation digest bound to the revision id; `hubctl capability` stays for unmanaged/operator use.

Tests (red first):
- `TestDeriveProfileFromRevision` (normative tools + grants → expected selections/allows/core)
- `TestDerivedProfilePinsBoundDefinitionDigest` (the M29 acceptance failure)
- `TestDerivedProfileDropsToolWithoutRequiredRight`

### S6.4 Live session update (R6.3)

Anchors: `gateway_projection.go` `refreshProjection`; toolhub store reload; MCP `tools/list_changed` emission; `hermes_capability.go` `disabledToolsets`.

Change:
1. Store reload after apply triggers refresh for every open session of the principal; changed lists emit `notifications/tools/list_changed`.
2. `via: native` toolset set changes are classified `respawn` and rendered into `disabled_toolsets` from the passport.
3. Prompt pointer lists core toolset names, states that `tools_list` is authoritative.

Tests (red first):
- `TestLiveToolsetMoveEmitsListChanged` (session open → apply moves `ssh` to core → notification + new list)
- `TestExcludedToolDisappearsLive`
- `TestNativeToolsetChangeIsRespawnClass`

## Red-team checklist

- Agent edits `/config/tools.yaml` → ro mount; editing `passport-src` impossible; `tools.select` right required for agent-initiated apply.
- Invoke of an extended tool whose right was revoked → deny (same decision as direct).
- Profile from another principal's revision never accepted (envelope match).

## Acceptance

A6 live: open a real session (live model), list tools; apply moving `ssh`
from extended to core with `include: [ssh_hosts, ssh_read]` → the same
session receives `tools/list_changed`, `ssh_hosts`/`ssh_read` direct, no
other ssh tool; apply `connectors.exclude: [connector_remove]` → tool gone
live; native toolset change shows `respawn` and takes effect next turn.
No `hubctl capability` call needed anywhere in the procedure.

## Out of scope

New MCP wrappers for URL enforcement beyond existing browser grant;
packs (M27).

## Done checklist

- [ ] Tests red then green; `just check`; docker-check; docker-clean.
- [ ] hub-agent-tools bump, rebind, respawn; docs/tools.md, docs/routines.md, prompt contract.
- [ ] docs/services/toolhub.md (replace the manual publication sequence), docs/rbac.md.
- [ ] Issue closed; state done; folder moved.
