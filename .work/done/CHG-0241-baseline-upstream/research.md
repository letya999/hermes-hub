# M29 research baseline (2026-11-01)

The planning pass inspected `README.md`, `docs/index.md`, `docs/architecture.md`, `docs/spaces.md`, `docs/agent-home.md`, `docs/rbac.md`, `docs/services/model-relay.md`, Broker docs, the pinned ADR/SPEC records, the stage playbook, source anchors and CI. The attached six-passport design is the accepted target. No runtime integration proof is claimed here.

| Current source / state | Current consumer | M29 owner / action |
|---|---|---|
| `scope.yaml`, `agent.yaml`, `workspace.yaml`, legacy `settings.yaml` | `ReadEnvironment`, `RenderEnvironment`, Compose, supervisor | Map fields to template/user sources; derive six read-only passports; retire readers after migration. |
| `spaces/profiles.yaml`, `policies/profiles/*`, org profiles, `profile.lock` | `applyProfiles`, `resolveProvisioningProfile`, `hubctl profile` | One pinned template chain and provenance/diff; do not turn profile grants into authority. |
| `spaces/models.yaml`, org/user models, model selection JSON | `LoadModelRegistry`, model-relay | Per-space service ladder, account-bound auth refs, quota and switch grant; selection state remains private. |
| ToolHub `CapabilityPolicy`, `CapabilityProfile`, `Governance`, grant history | ToolHub admission/projection | Preserve trusted ledger and per-call authority; compile a simple grants snapshot, not duplicate writable policy. |
| `HubToolsDefinition` 5.10.0 and ToolHub dynamic definitions | ToolHub projection, `tools_list`, `invoke` | Typed catalog, whole-toolset core/extended, pin/version/rebind; M27 retains pack acquisition. |
| `generated/soul-effective.<env>.md`, Hermes config, implicit upstream files | supervisor and pinned Hermes | Verify real load paths, then one explicit layered prompt; M20 vault remains sole long-term memory. |
| `generated/skills-effective` and user/org/platform stores | skill tools, runtime and ToolHub | Keep M24 ownership; project read-only /skills and lazy discovery. |
| Broker provider refs, contracts, leases and separate CLIProxy `auths/` | Broker, CLIProxy, relay | Add bounded model-key/session delivery contracts without secret bytes in snapshots/DTOs. |
| ToolHub/Communication/Broker runtime state and journals | service-specific stores | Preserve; not interchangeable with redundant config JSON/YAML. ADR-0091 storage migration is independent. |

Existing issues to cross-link: M20 #418/#419/#299/#303; M27 #383/#385/#387; M28 #402/#405; workspace manifest #208; budgets #400; job binding #397. No provider login, external send, production rollout or code push was authorized by this planning request.

Unverified before implementation: actual pinned Hermes `--ignore-rules` behavior on every managed entrypoint, per-session CLIProxy quota telemetry/reset semantics, and credential pinning when one proxy hosts multiple sessions. Stage M29.0 must produce real evidence; later stages reject unsupported claims rather than masking them in YAML.

Planning validation: `go run ./cmd/devcheck docs` and `git diff --check` passed. The first `just check` failed when a Windows test could not execute its temporary `docker.exe` (`Access is denied`); the same isolated test passed on retry. A second full run reached Go linking and failed to execute the installed `ld.lld.exe` with the same Windows access error. These are local gate failures, not evidence of M29 runtime acceptance.

---

# S0.1 migration matrix (2026-11-02, verified against feat/m29-six-passport-contract)

Field-level ownership. "Move" = field becomes a template/user source compiled
into the named passport; "preserve" = state store stays as-is; "retire" =
reader removed after migration (D7). Secret-bearing files are never
serialized into passports.

## scope.yaml — `{kind, id, organization}`

- Consumer: `ReadScope` (supervisor binding, `ReadEnvironment`,
  `ReadOrganization` cross-check), `InitEnvironment*` writers.
- M29: `kind/id/organization` → `stack` passport identity (space, org ref,
  environment). Single owner; retire the file after migration. Group/org
  scopes keep their executable contract (D7) — never retyped silently.

## agent.yaml / workspace.yaml / settings.yaml — `Settings`

| Field(s) | Consumer today | M29 passport / action |
|---|---|---|
| `schema`, `user`, `organization` | identity across render+supervisor | `stack` identity; retire |
| `model`, `model_url`, `summary_model`, `judge_model`, `cliproxy`, `cliproxy_credentials` | `Config`, `LoadModelRegistry`, model-relay | `models` (service ladder, auth refs); retire |
| `tools` (`via/access/scope/rules`), `mcp_servers` | `Config`, ToolHub projection, `usesDockerSocket` | `tools` (whole toolsets core/extended); retire |
| `hooks`, `memory`, `honcho`, `honcho_url`, `global_skills_dir` | `Config`, `writeHonchoConfig`, skills materialization | `config` runtime sections; managed forbids all three today |
| `ingress`, `telegram_auth` | communication render | `config` channels |
| `capability_mode`, `capability_profile_id`, `capability_generation` | `Validate`, managed spawn gate | template pin + provenance (`tools`/`stack` metadata); retire as free fields |
| `cli_registries`, `cli_runtime`, `cli_per_principal_uid`, `cli_toolboxes` | cli policy, controller render | `config` cli policy |
| `hosting`, `image_gen`, `web`, `compression`, `context`, `goals` | `Config` feature sections | `config` feature sections |
| `google_email`, `gitlab_host`, `desktop_url`, `drafts_url`, `oauth_port`, `browser_port`, `slack_events_port` | connector/port render | `config` connectors+ports |
| `infra`, `diagnostics` | `RendersInfra`, compose topology | `stack` service instances/modes |
| `workspace.mounts`, `workspace.goal`, `workspace.bootstrap_revision` | mount validation, goal bootstrap | `workspace` (writable/read_only/mounts) |
| `secrets.<env>.env`, `runtime.auth`, `toolhub.auth`, `model-relay.auth`, `media.auth`, `supervisor.auth`, `browser-novnc.passwd`, `meet-node-token.json` | env render, supervisor binding | never serialized — broker contracts/protected env; preserve |
| `communication.<env>.env`, `communication.users.yaml` | gateway env + channel users | `config` channels (non-secret) + preserve secret/state files |
| `execution.<env>.json` | execution-mode selection record | `stack` mode; preserve until migrate |

Note: `workspace.yaml` on disk today duplicates a `tools`/`ingress` fragment
(legacy split writer) — the migration matrix treats it as one `Settings`
source with `agent.yaml`; conflicting duplicates are migration conflicts to
report, not silently merge (R11.1).

## profiles — `policies/profiles/<n>/*`, `<org>/profiles/*`, `spaces/profiles.yaml`, `profile.lock`

- Files per profile: `agent.yaml`, `workspace.yaml`, `SOUL.md`,
  `profile.yaml` (version), `grants.yaml` (capability-rule template),
  `skills.yaml` (pins).
- Consumers: `resolveProvisioningProfile`, `applyProfiles`,
  `assignedProfileName` (org assignment > profile.lock > "default"),
  `profileSkillPins`, `profileSoulLayer`.
- M29: one pinned chain platform template version → org template (narrowed
  deltas) → user `choice` fields; field classes replace `locked` flags.
  `grants.yaml` templates never become authority — grant issuance stays in
  the ToolHub ledger; profile grants compile into the `grants` passport as
  effective `allow` lines. `profile.lock` → provenance record, not a second
  override path. `spaces/profiles.yaml` (owners→profile mapping) → template
  assignment in stack sources.

## models — `spaces/models.yaml`, org/user `models.yaml`, `generated/model-registry/*`, selection JSON

- `providers.{url,key_env,credential_env,owners,broker_grants,allow_private}`,
  `models.{owners,ladder[]}`, `switch.{owner→none|logical|catalog}`,
  `runtime_model`.
- Consumers: `LoadModelRegistry` merge (shared→org→user), model-relay,
  `writeModelRegistry`.
- M29 → `models` passport: ordered model *services* each bound to one
  credential principal, ordered compatible models, `{of,amount,per}` limits,
  sharing, `models.switch` grant. Runtime selection state (model-relay
  session JSON) stays private — not a passport field.

## grants / governance — ToolHub `CapabilityPolicy`, `CapabilityProfile`, `Governance`, grant ledger, `toolhub-tokens.json`

- Consumers: ToolHub admission/projection, `toolGovernance` per-load view,
  `MaterializeToolPolicySnapshot`, org actions via `effectiveOrgActions`.
- M29: ledger stays the trusted per-call authority (D3); `grants` passport =
  read-only effective `allow: capability: [operations]` snapshot with issuer,
  scope and epoch metadata. Missing operation denies. Org write actions map
  to cataloged operations.

## ToolHub tools surface — `HubToolsDefinition` 5.10.0, dynamic connector definitions, `hermes_capability_inventory.json`, `tool-manifest/tool-policy` snapshots

- Consumers: ToolHub projection, `tools_list`/`invoke`, managed capability
  render (`ManagedCapabilityInventory`), readiness/admission.
- M29 → platform `capabilities`/`toolsets` catalogs (typed Go source, CI
  parity vs handlers+docs) and `tools` passport `core ≤20`/`extended`
  placement. M27 keeps pack acquisition.

## skills — `spaces/<u>/skills`, `<org>/skills`, `config/skills` (platform), `hermes/skills` legacy, `generated/skills*`

- Consumers: `MaterializeEffectiveSkills` (+meta/index), `skillCapabilities`,
  skill tools, `tier0Block` catalog in soul-effective, `.no-bundled-skills`.
- M29 keeps M24 ownership: platform/org/user stores → read-only `/skills`
  mount + lazy `skills_search`/`skills_list`. No duplicate authority.

## channels / prompt — `SOUL.md` (user), `<org>/SOUL.md`, profile `SOUL.md`, `ChannelContract`, `APIServerPlatformHint`, `hermes/memories/{MEMORY,USER}.md`, project `AGENTS.md` chain

- Consumers: `HealUserSoul`, `MaterializeSoulEffective` (contract→org→
  profile→persona→tier0), spawn ro mounts, upstream prompt builder.
- M29 → prepared read-only `/config/{system-prompt,SOUL,USER,AGENTS}.md`
  layered platform→org→user; upstream implicit loads bounded per the S0.2
  evidence below; M20 vault owns long-term memory (file_write cannot write
  the memory subtree).

## runtime/dynamic state — `hermes/*` (sessions, logs, caches, memories, channel_directory, gateway_state), `runtime/*`, `managed/<env>/*`, `watch/`, `diagnostics/`, `archive/`, `workspace/`, `cache/`, `broker/*`, `cliproxy/*`

- Persisted state, never config authority. Preserved by migration (D7) —
  never deleted as "extra JSON".

## generated/ — `compose.<env>.yaml`, `hermes-effective`, `soul-effective`, `tool-policy`, `tool-manifest`, `model-registry`, `skills*`, `plugins`

- All become outputs of the single six-passport revision (R2.2) — atomic
  publish, previous snapshot retained on failure.

---

# S0.2 pinned-Hermes context-loading evidence (2026-11-02)

Pin: `nousresearch/hermes-agent@869228ca` (0.21.0) inside
`hermes-hub-dev-local-hermes-runtime:local`. Method: real upstream code paths
executed in the image (`agent.prompt_builder.build_context_files_prompt`,
`tools.memory_tool.MemoryStore`, `run_agent.AIAgent` with the exact kwargs
`gateway/run_turn_runner._build_fresh_agent` passes), against a filesystem
layout replicating the managed spawn (ro `config.yaml`/`SOUL.md` binds, ro
tmpfs extension roots, writable `/state/home`, `/state/hermes/memories`,
`/workspace`). Markers were distinct safe strings per source. This is unit/
container evidence on the pinned build — not a dev-stack chat turn; that
remains for M29.12 acceptance.

## Load paths, proven

| Source | Container path | Agent-writable? | Loads on managed turn? | Mechanism |
|---|---|---|---|---|
| soul-effective | `/state/hermes/SOUL.md` | no (ro bind) | yes — identity slot | `load_soul_identity=True` (run_turn_runner) + `load_soul_md` |
| effective config | `/state/hermes/config.yaml` | no (ro bind) | yes — drives everything below | host-materialized, ro mount |
| MEMORY.md | `/state/hermes/memories/MEMORY.md` | yes | **unmanaged only** — managed render sets `memory_enabled: false` | `MemoryStore` gated by `memory.memory_enabled` |
| USER.md | `/state/hermes/memories/USER.md` | yes | **unmanaged only** — managed sets `user_profile_enabled: false` | same store, `user_profile_enabled` |
| project context | context cwd (`terminal.cwd`; absent under managed → `resolve_placeholder_terminal_cwd` → `MESSAGING_CWD` else `Path.home()`=`/state/home`) | **yes** (`/state/home`, `/workspace` unmanaged) | **yes** | `build_context_files_prompt` walks cwd→git root; first wins `.hermes.md`→`AGENTS.md`→`CLAUDE.md`→`.cursorrules`; `_scan_context_content` blocks only known injection phrasing — a benign-worded override loads verbatim (proven: `MARKER_AGENTS_004` in prompt; trigger-phrase variant blocked) |
| platform hint | `agent.platform_hints.api_server.replace` | no (ro config) | yes — context tier at call time | `ephemeral_system_prompt`, never cached in the frozen prompt (proven: absent from `_build_system_prompt`, passed as `system_message` per call) |
| skills catalog | tier0 `<available_skills>` inside soul-effective + `/skills` ro view | no | yes (text, not toolset) | `tier0Block`; upstream `skills` toolset disabled + roots sealed |
| extension roots | `/state/hermes/{skills,hooks,plugins,skill-bundles,scripts,bin,node,lsp}` | no (ro tmpfs) | no | `managedExtensionRootTmpfs` + `HERMES_BUNDLES_DIR`/`HERMES_ENABLE_PROJECT_PLUGINS=0` preflight |

## `--ignore-rules` / `HERMES_IGNORE_RULES` — chat path only

- Flag exists in the pinned CLI (`hermes --help` lists it) and
  `HERMES_IGNORE_RULES=1` is honored — but both are consumed in `cli.py` /
  `cli_agent_setup_mixin.py`, mapping to `AIAgent(skip_context_files=True,
  skip_memory=True)` on the interactive/chat path only.
- The managed entrypoint is `hermes gateway run --no-supervise --force`;
  the gateway runner never reads `ignore_rules`. Its equivalent levers are
  `gateway.platforms.<plat>.skip_context_files` (context files only — proven
  in `run_turn_runner._skip_context_files`) and the render-side
  `memory.memory_enabled`/`user_profile_enabled` flags. There is **no
  gateway `skip_memory` lever** — memory suppression is a render decision.
- `load_soul_identity=True` is unconditional on fresh gateway agents: SOUL
  survives `skip_context_files` (proven: skip run kept SOUL, dropped
  MEMORY/USER/AGENTS).

## Red-team result

A project `AGENTS.md` in the context cwd attempts to override platform
restrictions **and succeeds at prompt level**: upstream loads it into
`# Project Context` whenever its phrasing avoids the injection scanner's
signatures. Under managed mode the cwd resolves to agent-writable
`/state/home`; unmanaged resolves to agent-writable `/workspace`. Only the
read-only SOUL/config/hint mounts are unforgeable prompt authority today.
Server-side authorization (ToolHub admission, mounts) never trusts the
prompt — consistent with ADR-0092 D5 — but the prompt contract is not yet
"exactly-once hub-owned rules".

## Fail-closed decision recorded

R9 stage (M29.9) must make prompt authority converge to hub-owned read-only
sources. Proven upstream levers, no upstream patch needed:

1. Render `gateway.platforms.api_server.skip_context_files: true` (or pin
   `terminal.cwd`/context cwd to a read-only-mounted directory) so agent-
   writable `AGENTS.md`/`CLAUDE.md`/`MEMORY`-adjacent files stop loading —
   while `load_soul_identity` keeps the ro SOUL (verified).
2. Keep `memory_enabled`/`user_profile_enabled` render-side for managed;
   prepared `USER.md` content lands via ro mount or soul layers, never via
   the writable store.
3. Preserve the `load_soul_identity` ro-mount contract; do not rely on
   `--ignore-rules` (chat-only) or `HERMES_IGNORE_RULES` (CLI-only) for
   managed suppression.
4. If any boundary stays unproven at implementation time, managed launch
   refuses (existing `managedPreflight`/`verifyManagedIsolation` shape) —
   the prompt is never the authorization boundary.

## Reconciliation with ADR-0092 / SPEC-0091 and M20/M27/M28

- D5's "verify all implicit load paths" is now closed evidence-wise:
  enumerated above with per-source writability. The "prepared contract
  reaches every managed turn exactly once" property holds for SOUL+hint
  today; project-context and memory-store paths are the gaps R9 must close.
- M20: vault/memory tooling owns long-term memory; upstream MEMORY.md is a
  separate agent-writable note path — managed already disables it, unmanaged
  spaces keep it until their migration. No conflict; do not conflate the
  stores.
- M27: pack skills arrive through the unified skillstore + effective view;
  no second prompt authority (tier0 block is render output).
- M28: shared edge does not touch prompt sources; multi-tenant isolation
  lives in mounts/networks, orthogonal.

## Validation

- `go test ./internal/stack/ -run TestManagedPromptSourcesInventory` — green;
  the test pins the managed source inventory (allowed path) and the
  heal/validate fail-closed cases (refusal/rollback).
- Container evidence commands and marker fixtures are reproducible under
  `.work/tmp/m29-spike/` (git-ignored); outputs quoted above verbatim.
