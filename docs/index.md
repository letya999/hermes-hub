---
description: Current documentation index and Memory Bank navigation.
last_verified: 2026-10-26
---
# Documentation

<!-- СОДЕРЖИМОЕ: validated by docs-check -->

## Orientation

| Document | Purpose |
|---|---|
| [principles.md](principles.md) | Project philosophy — the rules every other document assumes |
| [architecture.md](architecture.md) | System overview — roles, component map, networks, lifecycle |
| [boundaries.md](boundaries.md) | Per-component walls and system non-goals |
| [engineering.md](engineering.md) | Go conventions, gates, pins, change process |
| [testing.md](testing.md) | Test pyramid, evidence rules, E2E runner |
| [validation.md](validation.md) | What counts as evidence + the standing unproven boundary |
| [operations.md](operations.md) | Operator runbook — lifecycle, diagnostics, secrets, links, broker ops, migrations, backups, preflight |
| [glossary.md](glossary.md) | Canonical terms — feature, tool, toolset, capability, connector, backend/`via` |

## Access and surface

| Document | Purpose |
|---|---|
| [rbac.md](rbac.md) | Principals, grants, capability rules/profiles, governance evaluation, team bindings |
| [threat-model.md](threat-model.md) | Trust boundaries, threat table, deny-path requirements |
| [tools.md](tools.md) | Agent-visible tool catalog with action/resource/effect per tool |
| [spaces.md](spaces.md) | `agent.yaml`/`workspace.yaml` fields, space layout, feature registry |
| [agent-home.md](agent-home.md) | SOUL.md semantics, default skills, hook surface |
| [system-guide.md](system-guide.md) | Mandatory English reference skill, delivery paths and evidence boundaries |

## Integrations and channels

| Document | Purpose |
|---|---|
| [integrations.md](integrations.md) | Upstream pins and provider contracts |
| [prepared-connectors.md](prepared-connectors.md) | Connector pipeline, recipe ledger, tiers, per-connector runbooks |
| [channels.md](channels.md) | Channel adapter contract, conformance, how to add one |
| [channels/telegram.md](channels/telegram.md) | Telegram bot channel — files, commands, QR page |
| [channels/slack.md](channels/slack.md) | Slack App channel — Events API, HMAC, limits |
| [routines.md](routines.md) | Routine business tools, schedules, probes, delivery |
| [cli-policy.md](cli-policy.md) | Isolated terminal, private installs, operator catalog |
| [local-accounts-manager.md](local-accounts-manager.md) | Manager-friendly local Telegram/Google account setup |

## Per-service internals

| Document | Purpose |
|---|---|
| [services/hubctl.md](services/hubctl.md) | Operator CLI — render/up/down, secrets, migrations, connector host ops |
| [services/runtime.md](services/runtime.md) | Agent container process and relay modes |
| [services/toolhub.md](services/toolhub.md) | Capability gateway — admission, governance, lifecycle, audit |
| [services/hosting.md](services/hosting.md) | Per-user static sites, fragment shares and isolated stateless applications |
| [services/communication.md](services/communication.md) | Channel gateway — adapters, spool, `?t=` links |
| [services/supervisor.md](services/supervisor.md) | Scale-to-zero spawner — warm pools, control routing |
| [services/credential-broker/](services/credential-broker/index.md) | Copied MIT credential-authority module — русскоязычный под-дерево: architecture, engineering, operations, product, documentation |
| [services/media.md](services/media.md) | hub-stt/hub-tts/hub-media sidecars |
| [services/cell-proxy.md](services/cell-proxy.md) | Disposable workload cells and loopback relays |
| [services/model-relay.md](services/model-relay.md) | Per-space model selection and provider ladder |

## Records

| Document | Purpose |
|---|---|
| [adr/index.md](adr/index.md) | Append-only decisions and explicitly marked proposals |

Evidence and dated acceptance results live with their change records under
`.work/done/CHG-*/` — docs/ describes the current system, not its history.

<!-- КОНЕЦ СОДЕРЖИМОГО -->

Rules: [AGENTS.md](../AGENTS.md). Requirements: [specs](../specs/index.md).
Plans/progress: [.work](../.work/index.md). Contributor gates:
[CONTRIBUTING.md](../CONTRIBUTING.md). User setup: [SETUP.md](../SETUP.md).
This is a compact adaptation of the linked Memory Bank templates; empty template
directories and placeholder documents are deliberately omitted.
