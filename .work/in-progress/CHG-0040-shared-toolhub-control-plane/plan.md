---
description: "Один shared ToolHub/Broker/Controller на всех юзеров; изоляция через token→envelope и shared-сеть вместо дублирования стека на юзера."
last_verified: "2026-09-25"
---

# CHG-0040 Shared control plane, scoped compute

## Root cause

Render генерировал полный стек (toolhub, credential-broker, workload-controller,
cliproxy) в каждый `hermes-hub-<user>-<env>` Compose-проект с одинаковыми
loopback-портами (8090/8787/8545/8317). На одном хосте стартовал только первый
проект; spawned-рантаймы второго юзера уходили в per-user project network, где
`toolhub` не резолвится, и выходили наружу через
`host.docker.internal:8090` — в чужой ToolHub, который отвергал их bearer.
Итог для второго юзера: runtime падал на endpoint-валидации, позже — 401 на
каждый toolhub-вызов; агент уходил в ручную установку MCP в обход ToolHub
(clone + правка `/state/hermes/config.yaml`, затирается при перегенерации).

## Design

По модели oracle/vMCP: один control plane, эфемерный scoped compute.

- `settings.yaml` `infra: true|false` (default true): только infra-owning
  space рендерит toolhub/broker/controller/cliproxy и владеет портами.
- Все shared-сервисы и spawned-рантаймы — на external-сети
  `hermes-hub-runtime`; `HUB_TOOLHUB_ENDPOINT` дефолт `http://toolhub:8090/mcp`,
  `host.docker.internal` убран из дефолта.
- Supervisor больше не подставляет per-user project network — спавны идут на
  `m.cfg.Network`; per-user broker-secrets volume сохраняется по имени.
- ToolHub endpoint принимает N identities: `HUB_TOOLHUB_TOKENS_FILE` — JSON
  `token → identity.Envelope`, мержится с primary `HUB_TOOLHUB_TOKEN`,
  дубликаты/кривые токены отвергаются при старте. Bindings/workloads/
  credentials остаются ключованы (principal, context, runtime) — стор не менялся.
- Enroll второго юзера = запись его envelope в `spaces/<infra-owner>/
  toolhub-tokens.json` (bind-mount read-only в toolhub).

## Scope

- `internal/stack`: `Settings.Infra`, shared external network в render,
  endpoint default, mount/env для tokens-file.
- `internal/supervisor`: удалён per-user network override.
- `internal/toolhub`: `EndpointConfig.TokensFile` + `loadTokenEnvelopes`.
- `spaces/test-owner-2`: `infra: false`; удалён stray `google-calendar-mcp`
  clone и `google_calendar` из runtime config.yaml.
- `spaces/local`: endpoint override из secrets.dev.env удалён (default),
  `toolhub-tokens.json` с envelope test-owner-2.
- ADR-0026, architecture.md, регрессионные тесты.

## Non-goals

- Per-job attempt tokens (oracle-стиль) — статический bearer достаточен.
- Postgres-ledger; L2-изоляция рантаймов между собой — граница на authz.
