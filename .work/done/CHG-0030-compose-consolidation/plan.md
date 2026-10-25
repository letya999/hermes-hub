# CHG-0030: Консолидация hermes-hub в один compose и один образ

## Цель

Один образ `hermes-hub` содержит все компоненты. Один `compose.dev.yaml` поднимает
ровно 6 сервисов на localhost:

1. `hermes-runtime` — hub-runtime serve (как сейчас)
2. `communication-hub` — comm gateway (как сейчас)
3. `cliproxy` — cli-proxy-api (новый бинарь в образе, upstream router-for-me/CLIProxyAPI v7.2.154)
4. `toolhub` — toolhub daemon :8090 (переезжает с хоста в контейнер)
5. `workload-controller` — hubctl connector generic-controller :8545 (переезжает с хоста)
6. `credential-broker` — hub-credential-broker :8787 (данные мигрируются из volume)

## Ключевые изменения

- `docker/Dockerfile`: стадия сборки cli-proxy-api (pinned commit ba7e558… = v7.2.154),
  docker CLI и thv из pinned образов, seccomp-профили в `/opt/hub/seccomp/`.
- `internal/toolhub`: `HUB_DOCKER_HOST_ROOT` — трансляция путей контейнера в host-пути
  для sibling bind-mounts (preflight credentials, credential mounts, squid config,
  workspace volume).
- `internal/credentialbroker/client.go`: `<prefix>CA_FILE` — доверие self-signed CA брокера.
- `internal/stack/render.go`: +4 сервиса в compose(), генерация `generic-controller.json`,
  env-файлы, broker wiring (CONTROL/RUNTIME для toolhub, RUNTIME для runtime,
  APPROVE для communication).
- `spaces/local/settings.yaml`: `model_url` → http://cliproxy:8317/v1; career-go → toolhub.
- `spaces/local/secrets.dev.env`: HUB_TOOLHUB_ENDPOINT → http://toolhub:8090/mcp.

## Миграция данных

- `hermes-credential-broker-real-prod-20260918` → `spaces/local/broker` (bind,
  сохранить uid 10001, keys, ledger, secrets, contracts); TLS cert перегенерировать
  с SAN `credential-broker,localhost,127.0.0.1`.
- `hermes-credential-broker-real-20260918` → архивная копия `spaces/local/broker-archive`.
- `.local/cliproxy` → bind в cliproxy сервис (auths = OAuth-токены LLM-провайдеров).
- `spaces/local/runtime` → /state для toolhub/controller (store.json, credentials,
  artifacts, controller key — всё уже там).
- `communication-hub-data` volume → сохраняется, монтируется в comm-hub как сейчас.
- broker `runtime_dir` → `/state/materialized` — общий bind, видимый брокеру и
  контроллеру по одинаковому пути.

## Что удаляется (только hermes-hub)

- Контейнеры-сироты сборок: hermes-builder/proxy/seed/context/contract-*
- Мёртвые build-контейнеры со случайными именами (7 шт)
- `hermes-cliproxy`, `hermes-credential-broker-real(-prod)` после миграции
- Старые compose-контейнеры hermes-hub-local-dev-*
- Host-процессы toolhub.exe / hubctl.exe / hubctl-oauth.exe
- Volumes hermes-build-* / hermes-contract-* (orphans)
- ai-stp-* НЕ трогаем (другой проект)

## Проверка

- docker compose -f compose.dev.yaml up → 6 сервисов healthy
- smoke: runtime /healthz, comm :8081, cliproxy :8317, toolhub /mcp init,
  controller /admit auth, broker TLS handshake
- live prepare_source github-mcp-server (toolhub в контейнере собирает через socket)
- just check
