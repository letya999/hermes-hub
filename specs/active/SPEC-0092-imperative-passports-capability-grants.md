---
description: "Frozen M30 requirements: six passports as the imperative render/spawn/admission input, normative body schemas, agent capability grants (PDP/PEP), effect classes and live acceptance."
last_verified: "2026-11-15"
---

# SPEC-0092: Imperative passports and agent capability grants

Frozen: 2026-11-02. Design accepted; implementation not started.
Decision: [ADR-0093](../../docs/adr/ADR-0093-imperative-passports-capability-grants.md).
Supersedes in part: [SPEC-0091](SPEC-0091-six-passport-space-contract.md) R2.2
body shapes, R3.1, R4.1 tree, R5.1 vocabulary, R6.1/R6.2 language, R8.1 shape.
Stages: CHG-0254–0262 (M30). Gap evidence:
`.work/done/CHG-0254-passport-gap-audit/research.md`.

## Normative examples

The examples below are normative: the compiler must emit exactly these key
shapes for an equivalent source, and the golden tests parse them verbatim
(`internal/passport/testdata/normative/*.yaml`). Every file starts with:

```yaml
apiVersion: six-passport/v2
kind: stack            # stack|config|models|workspace|tools|grants
metadata: {space: alice, environment: dev, revision: r-…, generated_at: …,
           template: platform/personal@3, catalogs: "4"}
```

No `body:` wrapper; contract keys are top-level. `updated_at`/`sources` move
to `space-audit` provenance, not the passport.

### stack.yaml

```yaml
identity: {user: alice, organization: acme}

services:
  hermes-runtime:       {mode: supervised}
  communication-hub:    {mode: shared}
  toolhub:              {mode: shared}
  credential-broker:    {mode: shared}
  model-relay:          {mode: per-space}
  vault:                {mode: per-space}
  cliproxy-google-1:    {mode: per-space}
  cliproxy-google-2:    {mode: per-space}
  cliproxy-codex:       {mode: per-space}
  hub-stt:              {mode: shared}
```

### config.yaml

```yaml
runtime:
  timezone: Europe/Moscow
  locale: ru
  context: {compaction: auto, parallel_lanes: 2}
supervision:
  idle_stop: 5m
  restart: on-failure
  watchers:
    stack-health: {protected: true, every: 1m}
ports:
  oauth-callback: 8008
  browser-vnc: 6081
channels:
  telegram:
    bot: {ref: telegram-bot-alice}
    allowed_users: {ref: telegram-allow-alice}
    voice: {in: transcribe, out: tts}
memory:
  backend: vault
  nightly_rollup: "03:00"
```

### models.yaml

```yaml
default: everyday

routes:
  everyday:
    ladder: [google-pro-1, google-pro-2, codex-pro, openrouter-key]

services:
  google-pro-1:
    via: cliproxy
    endpoint: http://cliproxy-google-1:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: google-one-1}
      proxy_access: {kind: broker-key, ref: cliproxy-google-1}
    sharing: personal
    limits:
      - {of: provider_quota, amount: 50, unit: percent, per: 1d, clock: provider}
    models:
      - {id: gemini-3.8-flash-high, reasoning: high}
      - {id: gemini-3.7-flash-high, reasoning: high}
  google-pro-2:
    via: cliproxy
    endpoint: http://cliproxy-google-2:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: google-one-2}
      proxy_access: {kind: broker-key, ref: cliproxy-google-2}
    sharing: personal
    limits:
      - {of: provider_quota, amount: 100, unit: percent, per: 1d, clock: provider}
    models:
      - {id: gemini-3.8-flash-high, reasoning: high}
      - {id: gemini-3.7-flash-high, reasoning: high}
  codex-pro:
    via: cliproxy
    endpoint: http://cliproxy-codex:8317/v1
    auth:
      upstream: {kind: oauth-session, ref: codex-pro-1}
      proxy_access: {kind: broker-key, ref: cliproxy-codex}
    sharing: personal
    limits:
      - {of: requests, amount: 150, per: 5h}
      - {of: tokens, amount: 1200000, per: 1w}
    models:
      - {id: gpt-5.6-sol, reasoning: high}
  openrouter-key:
    via: openai-compatible
    endpoint: https://openrouter.ai/api/v1
    auth:
      upstream: {kind: broker-key, ref: openrouter-alice}
    sharing: personal
    limits:
      - {of: spend_usd, amount: 10, per: 1d}
      - {of: spend_usd, amount: 40, per: 1w}
    models:
      - {id: google/gemini-3.8-flash, reasoning: high}
```

### workspace.yaml

```yaml
tree: |
  /
  ├── config/                         [только чтение]
  │   ├── stack.yaml
  │   ├── config.yaml
  │   ├── models.yaml
  │   ├── workspace.yaml
  │   ├── tools.yaml
  │   ├── grants.yaml
  │   ├── system-prompt.md            итоговый системный промпт
  │   ├── SOUL.md
  │   ├── USER.md
  │   └── AGENTS.md
  ├── workspace/
  │   ├── inbox/                      оригиналы того, что пришло извне
  │   │   ├── people/<channel>/<date>/<attachment-id>/
  │   │   └── services/<service>/<date>/<attachment-id>/
  │   ├── work/                       рабочие файлы
  │   ├── projects/                   проекты Alice, включая вложенные Git-репозитории
  │   │   ├── website/.git/
  │   │   └── research/.git/
  │   ├── artifacts/                  то, что создал агент или сервис
  │   │   ├── documents/
  │   │   ├── images/
  │   │   ├── videos/
  │   │   ├── archives/
  │   │   └── services/<service>/
  │   └── outbox/                     подготовленные к отправке файлы
  ├── vault/
  │   ├── memory/
  │   │   ├── CORE.md
  │   │   ├── CORE.history.md
  │   │   ├── daily/
  │   │   ├── cards/{contacts,projects,decisions,ideas,notes}/
  │   │   └── summaries/{daily,weekly,monthly,yearly}/
  │   ├── library/
  │   └── attachments/
  ├── skills/                         [только чтение; собранный каталог]
  └── shared/acme-docs/               [только чтение]

writable:
  - /workspace/work
  - /workspace/projects
  - /workspace/artifacts/documents
  - /workspace/artifacts/images
  - /workspace/artifacts/videos
  - /workspace/artifacts/archives
  - /workspace/outbox
  - /vault/library
  - /vault/attachments

read_only:
  - /config
  - /workspace/inbox
  - /workspace/artifacts/services
  - /vault/memory
  - /skills
  - /shared/acme-docs

mounts:
  /shared/acme-docs: {source: org/acme/docs, mode: ro}
```

### tools.yaml

```yaml
core:
  discovery:
    via: toolhub                 # system_guide, tools_list, invoke
  files:
    via: toolhub
    scope: {paths: [/workspace, /vault, /skills, /shared/acme-docs]}
    rules:
      file_write:
        scope: {paths: [/workspace/work, /workspace/projects,
                       /workspace/artifacts, /workspace/outbox, /vault/library]}
  skills:
    via: toolhub
    access: read

extended:
  ssh:
    via: toolhub
    tools: {include: [ssh_hosts, ssh_read]}
    scope: {hosts: [staging], paths: [/var/log/app]}
  connectors:
    via: toolhub
    tools: {exclude: [connector_remove]}
    access: read
  watchers:
    via: toolhub
  browser:
    via: mcp:playwright
    access: read
    scope: {urls: [https://docs.acme.example]}
```

### grants.yaml

```yaml
allow:
  workspace:     [read, write, create_project]
  memory:        [read, search, remember]
  models:        [list, use, switch]
  tools:         [list, select]
  skills:        [list, invoke, draft]
  connectors:    [discover, prepare, enable]
  agents:        [inspect_self, restart_self, monitor_self]
  stack:         [inspect]
  config:        [read, edit_own]
  channels:      [send_voice]
  observability: [read_own_logs]
```

## Capability vocabulary (catalog release 4)

A capability is an agent right on a scope, never a tool. Every operation
declares `subject` (self|cross), `scope` (none|path|model|toolset|skill|
connector|service|channel|log), `issuers`, `pep` and `effect`.

| Capability | Operations (self) | Cross-user ops | PEP |
|---|---|---|---|
| workspace | read, write, create_project | read_other | file tools, project init |
| memory | read, search, remember | — | vault tools (M20) |
| models | list, use, switch | — | model-relay |
| tools | list, select | — | ToolHub projection, apply |
| skills | list, invoke, draft, publish | — | skill tools, skillstore |
| connectors | discover, prepare, enable, disable, remove | share | ToolHub control ops |
| agents | inspect_self, restart_self, monitor_self | inspect_other | supervisor |
| users | — | list, invite | hubctl/communication |
| stack | inspect, change | — | stack_inspect tool, apply |
| config | read, edit_own | — | selfsettings, apply |
| channels | send, send_voice | — | communication-hub |
| observability | read_own_logs | read_logs | diagnostics |

Tool requirements: every toolset member declares `requires:
[capability.operation, …]` (e.g. `file_write` → `workspace.write`,
`settings_set` → `config.edit_own`, `skill_manage` → `skills.draft`).

## Effect classes

| Change | Class | Consumer |
|---|---|---|
| tools core/extended/rules/scope (`via: toolhub`, `mcp:*`) | live | ToolHub projection + `tools/list_changed` |
| tools `via: native` set | respawn | Hermes `disabled_toolsets` |
| grants allow / revoke | live | PDP epoch on next call |
| workspace writable/read_only | live | file-tool PEP reads current revision |
| workspace tree/mounts | respawn | supervisor mount plan |
| models default/routes/limits/models | live | model-relay registry reload |
| models service auth contract | recreate | broker lease into that service |
| config runtime | respawn | effective Hermes config |
| config ports/channels | recreate | compose services |
| config supervision | live | supervisor policy reload |
| stack services/mode | rebuild | `hubctl build` + compose project |

## Requirements

### R0. Gap audit and typed schema
- R0.1: The audit names, per passport field, its current consumer (or none)
  and the legacy source actually used; it is the stage-0 receipt.
- R0.2: Typed Go schema for all six v2 bodies with strict decode (unknown
  key fails); normative examples parse and round-trip byte-stable.
- R0.3: A consumer registry maps every schema leaf to a consumer and effect
  class; a leaf without consumer fails the test suite.

### R1. Apply and reconcile
- R1.1: `hubctl space apply --dir spaces/<u> --env <e> [--confirm]` compiles,
  prints a diff grouped by effect class, publishes on confirm and reconciles.
- R1.2: Reconcile runs the minimum action per class; a failed reconcile
  reverts `current` to the previous revision and reports the failed step.
- R1.3: `/config` is a bind-mounted directory refreshed atomically per
  revision; consumers read the current revision, never a cached copy.

### R2. Capability grants
- R2.1: Catalog release 4 ships the capability vocabulary above with
  operation semantics; tool-level ids become toolset `actions`.
- R2.2: One PDP (`toolhub` grants service) answers `Decide(envelope, cap,
  op, scope)` from ceilings + ledger + epoch; every PEP in the table calls it
  except the bounded operator apply effect in R2.4.
- R2.3: `grants.yaml` is compiled from ceilings ∩ ledger; templates never
  grant; an empty allow for an admitted space is a compile error.
- R2.4 (2026-11-08): While an authenticated `space apply` journal is in its
  effects phase, the supervisor may respawn only the journal's owner runtime
  when the request carries that exact revision and the published pointer names
  the old or new revision. The ToolHub PDP denies ordinary calls while the
  journal exists; this bounded operator effect must not depend on that denied
  call. A missing or mismatched journal uses the normal `agents.restart_self`
  decision.

### R3. Stack
- R3.1: `stack.yaml` has `identity` and `services: {instance: {mode}}`;
  instance names resolve to catalog ids; modes are validated against the
  catalog `instances` class; dependencies are derived, not emitted.
- R3.2: Compose project, image build set and supervisor spawn selection are
  rendered from the stack passport only; adding/removing a per-space
  instance adds/removes exactly that container.

### R4. Config
- R4.1: `config.yaml` carries runtime, supervision, ports, channels, memory;
  channel credentials are refs only; ports are unique per host.
- R4.2: Effective Hermes config, supervisor policy, communication-hub
  channel config and compose ports are rendered from the config passport.

### R5. Workspace
- R5.1: Source declares a typed tree; the passport emits one ASCII `tree`,
  `writable`, `read_only`, `mounts`; no second list representation.
- R5.2: Compose and supervisor create the runtime container from the mount
  plan: `/config` (six passports, system-prompt, SOUL/USER/AGENTS), every
  `/workspace` subtree, `/vault`, `/skills`, `/shared/*` per `mounts`.
- R5.3: File-tool PEP enforces longest-prefix writable/read_only per call
  from the current revision (read_only wins; symlink/rename escapes deny).

### R6. Tools language and live projection
- R6.1: Only `core` and `extended`; values are toolsets with `via`,
  `tools.include|exclude`, `access`, `scope{paths,hosts,urls}`,
  `rules.<tool>.scope`; discovery pinned in core; overflow moves whole
  toolsets; core ≤ 20 tools.
- R6.2: ToolHub policy/profile/binding are derived from the revision at
  apply (confirmed), never hand-authored; tool admission = toolset selected
  ∧ required grants present ∧ scope matches.
- R6.3: A `live`-class tools change reaches an open session as
  `tools/list_changed` with the new list, without runtime restart.

### R7. Models, sessions and Broker
- R7.1: `models.yaml` matches the normative shape; a `via: cliproxy` service
  requires a stack instance whose host equals its endpoint host.
- R7.2: `auth.upstream`/`auth.proxy_access` resolve to Broker contracts
  delivered only to that service's instance or the relay; OAuth session login
  (`hubctl models login <service>`) stores the session in Broker.
- R7.3: model-relay registry renders from the passport; route ladder →
  services → models; unmeasurable limits disable the service; `models.switch`
  gates route switching.

### R8. Retirement, migration and acceptance
- R8.1: Render refuses a managed space without a revision; legacy authority
  files are read only by `space-migrate` (v2 mapping to the new shapes).
- R8.2: Docs (spaces, rbac, toolhub, model-relay, operations, hubctl,
  architecture, tools) describe the imperative contract and effect classes.
- R8.3: Live acceptance on the dev stack proves every effect class with a
  real model and real cliproxy sessions.

## Acceptance

| ID | Observable condition |
|---|---|
| A0 | Audit table complete; normative examples parse; registry test fails on an unconsumed leaf. |
| A1 | Apply shows a classified diff; a forced reconcile failure restores the previous `current`. |
| A2 | Revoking `workspace.write` makes the next `file_write` deny in the open session; `grants.yaml` lists the ledger. |
| A3 | Adding `cliproxy-google-2` to stack creates exactly that container; removing it removes it. |
| A4 | Changing `config.ports.oauth-callback` recreates only the owning service. |
| A5 | Runtime container mounts match `workspace.yaml`; write to `/workspace/inbox` denies; narrowing writable is live. |
| A6 | Moving `ssh` from extended to core, or excluding a tool, changes the live session list without restart. |
| A7 | Two OAuth sessions + one API key route per ladder; exhausting service 1 falls to service 2; switch without grant denies. |
| A8 | Ten spaces from the normative templates pass A2–A7 with real model calls; no legacy file is read. |

## Boundary

M20 owns vault internals (memory ops are cataloged; PEP lands with M20
tools). M27 owns packs. M28 owns shared edge. No upstream Hermes patch, no
guessed provider API, no secret bytes in passports.
