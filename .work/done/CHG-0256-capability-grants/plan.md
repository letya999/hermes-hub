# CHG-0256: Agent capability vocabulary, PDP and PEPs (M30 stage 2)

Chain: ADR-0093 D3, D4 -> SPEC-0092 R2 (capability table) -> this plan -> `state.yaml`.
Evidence: `../../done/CHG-0254-passport-gap-audit/research.md` Finding 3.
Previous stage: CHG-0255. Next stage: CHG-0257.

## Goal

Grants become pseudo-RBAC for the agent: `grants.yaml` lists real rights
(`workspace: [read, write, create_project]`, `models: [list, use, switch]`,
`agents: [restart_self]` …) compiled from ceilings and the trusted ledger,
and every place that performs such an action asks one decision point before
acting. Tools stop being "capabilities": a tool only declares which rights
it needs.

## Read before coding

1. SPEC-0092 capability table and normative `grants.yaml`.
2. `docs/rbac.md` (governance + capability policy layers, confirmations).
3. `internal/catalog/catalog.go`, `cmd/cataloggen/main.go`, `platform/catalogs/3/capabilities.yaml`.
4. `internal/toolhub/api/grants.go` `Grant`, `internal/toolhub/grants.go`, `cmd/hubctl/grant.go`.
5. `internal/agenttools/tools.go` `HubToolsDefinition`; `internal/stack/prompt_contract.go`; `docs/tools.md`.
6. PEP sites: `internal/selfsettings/selfsettings.go` `Set`; `internal/runtime/model_relay.go` `switchLevel`/`serveSelect`; `internal/supervisor/http.go`; `internal/communication` voice send path; diagnostics log reads.

## Preconditions and rules

- Agent-tool contract changes ship together: description, prompt-contract guidance, docs/tools.md + docs/routines.md, bumped `hub-agent-tools` version, rebind + respawn.
- `IssuedBy` model/hermes stays refused; templates never grant.
- Red test first.

## Slices

NOTE (updated by CHG-0255 S1.0): grants `allow` vocabulary/emission is v2-shaped already; this stage replaces the capability vocabulary with the rights registry (release 4) and builds PDP/PEP.

### S2.1 Rights vocabulary in catalog release 4 (R2.1)

Anchors: `internal/catalog/catalog.go` (new `Rights` registry + `Docs.ValidateOperation`); `cmd/cataloggen/main.go`; `platform/catalogs/4/`.

Change:
1. Typed `Right{Domain, Op, Subject(self|cross), Scope, Issuers, PEP, Effect}` for every row of the SPEC table; emit `capabilities.yaml` release 4 as `capabilities: {workspace: {operations: {read: {...}}}}`.
2. Move per-tool ids (`files.read` …) into `toolsets.yaml` as `actions`; each toolset member gets `requires: [domain.op]`.
3. `ValidateOperation(domain, op)` checks the rights vocabulary; tool-action validation moves to `ValidateAction`.

Tests (red first):
- `TestRightsCatalogMatchesSpecTable`
- `TestEveryRightHasRegisteredPEP` (PEP ids from a registry populated in S2.4; pending ids fail after this stage)
- `TestEveryToolDeclaresRequires` (every `HubToolsDefinition` tool)

### S2.2 Tool requirements in the agent-tools contract (R2.1)

Anchors: `internal/agenttools/tools.go` `HubToolsDefinition`; `internal/stack/prompt_contract.go`; `docs/tools.md`, `docs/routines.md`.

Change:
1. Add `Requires []string` to tool specs (`file_write` → `workspace.write`, `settings_set` → `config.edit_own`, `skill_manage` → `skills.draft`, `connector_enable` → `connectors.enable`, …).
2. Bump `hub-agent-tools` version; prompt contract states that missing rights hide tools and `tools_list` shows the reason.

Tests (red first):
- `TestHubToolsRequiresAreCatalogRights`
- docs parity test (existing) passes with the new field documented.

### S2.3 PDP and ledger (R2.2, R2.3)

Anchors: new `internal/toolhub/rights.go`; `internal/toolhub/api/grants.go` (`GrantKindCapability`, `Capability`, `Operation`, `Scope`); `internal/toolhub/api/http` control route; `cmd/hubctl/grant.go`; `internal/passport/passport.go` `projectGrants`.

Change:
1. Ledger grant kind `capability` with `{domain, op, scope?, expires_at?}` under confirmation; revoke bumps the principal's rights epoch.
2. `Store.Decide(env, domain, op, scope) Decision{Allow, Reason, Epoch}`: ceilings (platform/org from passport sources) ∩ active ledger; cross ops require `subject: cross` grant naming the target principal.
3. Control endpoint `POST /v1/rights/decide` (runtime or service bearer, envelope-derived principal only) for out-of-process PEPs.
4. `projectGrants` compiles `allow` from ledger export ∩ ceilings; admitted managed space with empty allow → compile error.
5. `hubctl grant --dir … --cap <domain> --op <op> [--scope …] --confirm` / `--revoke`.

Tests (red first):
- `TestDecideDeniesMissingOperation`
- `TestDecideRevokeEpochImmediate` (revoke → next Decide denies without republish)
- `TestCrossUserOperationIsSeparate`
- `TestTemplateLayerCannotGrant`, `TestGrantsSnapshotFromLedger`
- HTTP: `TestRightsDecideRejectsForeignPrincipal` (body principal ignored, envelope wins)

### S2.4 PEP wiring (R2.2)

Anchors and change (each PEP calls `Decide`, deny → localized reason via `internal/locale`):
1. ToolHub admission (`capability_store.go` `managedSelectionLocked`): a tool projects only if all `Requires` allow — direct and invoke paths share it.
2. `selfsettings.Set` caller (agenttools `settings_set`): `config.edit_own`; `settings_get`: `config.read`.
3. model-relay `serveSelect` and `/v1/models`: `models.switch` / `models.list`; request routing: `models.use`.
4. supervisor self-restart/status routes: `agents.restart_self`/`inspect_self`/`monitor_self` (new agent tools `agent_status`, `agent_restart`, `agent_logs` if absent — part of the S2.2 bump).
5. communication voice send: `channels.send_voice`.
6. diagnostics own-log read: `observability.read_own_logs`.
7. `memory.*` and `workspace.create_project`: registered PEP ids `pending:M20` / `pending:CHG-0259`.

Tests (red first): `TestSettingsSetRequiresEditOwn`, `TestRelaySwitchRequiresGrant`,
`TestSupervisorRestartSelfRequiresGrant`, `TestVoiceSendRequiresGrant`,
`TestToolHiddenWhenRequiredRightMissing` (direct + invoke).

## Red-team checklist

- Agent writes `grants.yaml` or `passport-src` → no authority change (files ro / not mounted; snapshot is not read by PDP).
- Request body carries another `principal_id` → envelope wins, deny for cross ops.
- Template layer carries `allow` → compile error.
- Revoke while a session is open → next call denies before respawn.
- Deny reasons and audit show domain/op/epoch only — no scopes containing secrets.

## Acceptance

A2 live: on two dev spaces grant `workspace.write` to one only; in the open
session `file_write` works for one and is hidden/denied for the other;
revoke → next call denies without respawn; `grants.yaml` shows both states
after apply. Model switch without `models.switch` returns the localized deny.

## Out of scope

M20 memory tool internals; user management UI.

## Done checklist

- [ ] Tests red then green; `just check`; docker-check (agent-tools + relay + supervisor changed); docker-clean.
- [ ] Rebind + respawn managed spaces with the new hub-agent-tools version.
- [ ] docs/rbac.md, docs/tools.md, docs/routines.md, docs/services/{toolhub,model-relay,supervisor}.md updated.
- [ ] Issue closed; state done; folder moved.
