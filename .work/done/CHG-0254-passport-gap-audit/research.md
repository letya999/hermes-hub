# CHG-0254 research: M29 passport gap audit (2026-11-02)

Evidence for ADR-0093 / SPEC-0092 R0.1. Sources: published revision
`spaces/artem_two/generated/passports/r-256669d55c81fd23/`, code on
`feat/m29-six-passport-contract`, ten-space acceptance log in
`.work/done/CHG-0253-acceptance-ci/state.yaml`.

## Finding 1 — nothing consumes the revision

`grep internal/passport|generated/passports` over `*.go`: the only
non-test, non-migration reader is `internal/stack/materialize.go:63`
(`prompt-pointer.txt`). Actual sources of behaviour:

| Behaviour | Real input | Anchor |
|---|---|---|
| Settings for render | `agent.yaml` + `workspace.yaml` (+`settings.yaml`) | `stack.ReadEnvironment` (`internal/stack/space.go:309`), called from `RenderEnvironment` (`render_env.go:61`) |
| Compose services/images | `Settings` flags (`Infra`, `Cliproxy`, `Has(...)`) | `stack.compose` (`compose.go:52`), `imageBuild` (`compose.go:266`) |
| Runtime mounts (managed) | hardcoded list | `supervisor/ensure.go:503`, `:658`; compose `compose.go:61-93` |
| Writable/read-only | `HUB_WORKSPACE_RO` env from `workspaceReadOnlyPaths` | `tools.go:263`, `compose.go:146` |
| Model routing | `spaces/models.yaml` + space `models.yaml` merge | `LoadModelRegistry` (`models.go:105`), `writeModelRegistry` (`materialize.go:96`), relay `model_relay.go:526` |
| cliproxy | one instance, shared or dedicated, auth dir copied by operator | `compose.go:513`, `:662` |
| Tools the agent sees | hand-published ToolHub policy/profile/binding + `governance.json` | `cmd/hubctl/capability.go`, `toolhub/capability_store.go:209` |
| Native toolsets | `disabledToolsets(Settings)` | `hermes_capability.go:92` |
| Grants | ToolHub section grants (`governance.json`) | `stack/governance.go:220` |

The ten-space acceptance needed policy/profile records copied and narrowed
from `artem_main`, digest re-pinning to the bound `hub-agent-tools`
version, and manual `agent-tools` binding — direct proof that `tools.yaml`
does not drive the projection.

## Finding 2 — body shapes do not match the task

| Passport | Expected (task/SPEC-0092) | Emitted | Gap |
|---|---|---|---|
| stack | `identity`, `services: {name: {mode}}` | `features`, `mode`, `organization`, flat `services` list | no identity; no per-service mode; `features` belongs to tools/config; multi-instance (`cliproxy-google-1`) impossible |
| config | runtime, supervision, ports, channels, memory | `memory`, `timezone` | runtime/supervision/ports/channels missing (classes exist in `passport.go:106` but migrator never fills them) |
| models | `default`, `routes.*.ladder`, `services.*.{via,endpoint,auth,sharing,limits,models}` | `default: cliproxy`, `services: [cliproxy]` | entire route/service language missing; relay schema (`ModelHopRef`) exists but is not projected |
| workspace | one ASCII `tree`, narrow `writable`, `read_only`, `mounts` | `layout` + flat `tree` duplicate, `writable: [/workspace]` | `/config`, `/vault`, `/shared` absent; inbox writable (over-grant); no mounts |
| tools | toolsets in `core`/`extended` with via/tools/access/scope/rules | tool names in `core`, section ids in `extended`, internal `sections`, `catalog` | language absent; levels confused; `discovery` listed as tool names |
| grants | `allow: {capability: [ops]}` from a rights vocabulary | `allow: {}` | no vocabulary: catalog "capabilities" are per-tool ids (83 entries, `platform/catalogs/3/capabilities.yaml`) |

## Finding 3 — capability ≠ tool

`platform/catalogs/3/capabilities.yaml` families: archives, artifacts,
assignments, context, documents, exposure, files, goals, hh, hosting,
image_gen, images, inbox, knowledge, routines, services, settings, skills,
skills_manage, ssh, terminal, watches — one-to-one with toolsets. Missing
agent rights with no tool equivalent: models.switch (relay `switchLevel`,
`model_relay.go:730`), agents.restart_self (supervisor), channels.send_voice
(communication), observability.read_own_logs (diagnostics),
stack.inspect, config.edit_own (`internal/selfsettings`), tools.select.

## Finding 4 — live vs respawn mechanics already available

- ToolHub projection refresh + `notifications/tools/list_changed`
  (`toolhub/gateway_projection.go:209`, docs/services/toolhub.md Endpoint):
  live for `via: toolhub`/`mcp`. Fixed during acceptance: refresh honours the
  core bound (`TestToolsetCoreBoundSurvivesRefresh`).
- model-relay reloads the registry on mtime change (`model_relay.go:676`):
  live for routes/limits once the file is passport-rendered.
- Native toolsets are baked into effective Hermes config → respawn.
- Docker Desktop single-file binds go stale on rename-replace (observed on
  `store.db`/token file inode change) → `/config` must be a directory bind.

## Finding 5 — M20 vault not implemented

`/vault` is not materialized or mounted anywhere (`grep vault internal/stack
internal/supervisor` empty; CHG-0204..0213 in progress). M30 materializes the
directory layout and mounts; memory operations are cataloged, their PEP
lands with M20 tools.

## Prior art

ADR-0089 (live inventory), ADR-0083 (lean images/packs), SPEC-0046,
docs/rbac.md (governance + capability policy layers), CHG-0248 broker
contracts `model-api-key.json` / `cliproxy-session.json`.
