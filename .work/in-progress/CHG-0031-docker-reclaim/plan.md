---
description: "Удержание диска: удаление superseded образов, dangling и сирот artifact-build."
last_verified: "2026-09-20"
---

# CHG-0031 Docker reclaim

## Root cause

Каждая пересборка hub-образа (~8.7GB) оставляла предыдущее поколение dangling:
`hermes-hub:0.3.0-dev` держал 7.53GB уникальных слоёв, удаление блокировали
остановленные compose-контейнеры на старом image ID. Глобальные
`DOCKER_BUILDKIT=0`/`COMPOSE_DOCKER_CLI_BUILD=0` загоняли сборки в classic
builder, а `hermes-build-state-shared` (opt-in `HUB_BUILD_CACHE=1`) рос до
7.13GB без чистки. Убитые artifact build оставляли `hermes-build-*` волюмы.

## Scope

- `devcheck docker-build IMAGE TARGET`: BuildKit сборка независимо от env.
- `devcheck docker-clean [--deep]`: stale `hermes-hub:*` теги вне
  `spaces/*/compose*.yaml` (тег `test` всегда сохраняется), остановленные
  `hermes-*` контейнеры на dangling образах, `image prune`, `builder prune`,
  сироты `hermes-builder-*/hermes-build-proxy-*/hermes-build-seed-*`,
  `hermes-build-net-*`, `hermes-build-*` волюмы; `--deep` также сносит
  `hermes-build-state-shared`.
- `just docker-check` завершается `docker-clean`; `just docker-clean` рецепт.
- Seed-контейнер получил label `hermes-hub.role=artifact-build-seed`.

## Boundaries

- Не трогаем ai-stp-* и чужие проекты; sweep ограничен hermes-* именами и
  label `hermes-hub.role`.
- Запущенные контейнеры и используемые ресурсы не удаляются; clean — только
  когда нет активного artifact build.
- Без изменения admission/авторизации ToolHub.

## Verification

- `go test ./internal/devcheck ./cmd/devcheck` — fake-runner покрытие sweep.
- `go run ./cmd/devcheck docker-clean --deep` live: -4 stale контейнера,
  -1 dangling образ 8.77GB, -3 волюма (state-shared 7.13GB + context/output).
- `just check` перед delivery.
