---
description: "Исключить non-credential env-имена из connection recipe и добавить reviewed gitlab-pat контракт."
last_verified: "2026-09-21"
---

# CHG-0034 Non-credential env filtering

## Root cause

`prepareSelfInstall` для zereight/gitlab-mcp отклонялся с
`no reviewed credential broker contract covers gitlab-mcp credentials`.
`connectionFieldFromEnv` помечал Required любое uncommented имя из
`.env.example` с пустым или secret-значением: `GITLAB_TOKEN_TEST` (тестовая
фикстура upstream), `HTTP_PROXY`, `HTTPS_PROXY` (их инжектит сам workload
runtime для egress-прокси), `GITLAB_ALLOWED_PROJECT_IDS=` (опциональный
non-secret knob). Дополнительно `server.json` объявляет настоящее имя
`GITLAB_PERSONAL_ACCESS_TOKEN` (isRequired), а устаревший `.env.example` —
`GITLAB_TOKEN`; сервер читает первое. `bindReviewedContract` требует покрытия
всех Required имён deliveries одного reviewed-контракта.

Дополнительно найдено на живом прогоне: `bindReviewedContract` строил
`CredentialContractEnv` только из deliveries, покрывающих Required-имена.
Опциональная delivery `api_url -> GITLAB_API_URL` отбрасывалась: значение,
введённое в форме брокера, хранилось, но не попадало в env workload'а, и MCP
уходил на дефолтный `gitlab.com` -> 401.

## Minimal scope

- `nonCredentialEnvName` в `recipe_resolver.go`: исключить `HTTP_PROXY`,
  `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY` и `TEST_*`/`*_TEST`/`*_TEST_*` из
  connection fields (действует и для .env.example, и для compose env).
- Эвристические env-источники помечают Required только secret-class имена;
  пустой non-secret knob остаётся optional полем, `isRequired` манифестов
  остаётся авторитетным.
- `bindReviewedContract`: env-мапа покрывает все deliveries на объявленные
  credential inputs (required + optional); уже привязанная definition
  добирает недостающие опциональные deliveries того же контракта, существующие
  маппинги не перезаписываются, чужой контракт не подставляется.
- Reviewed-контракт `gitlab-pat` доставляет один PAT под обоими upstream
  написаниями (`GITLAB_PERSONAL_ACCESS_TOKEN` и `GITLAB_TOKEN`) плюс
  опциональный `GITLAB_API_URL` для self-hosted; в `examples/contracts`
  credential-broker и в `contracts_dir` развёрнутого брокера.

## Regression boundaries

- `.env.example` формы gitlab-mcp: только `GITLAB_TOKEN` Required;
  `GITLAB_TOKEN_TEST`, прокси и `TEST_PROJECT_ID` отброшены.

## Documentation and specification

- `docs/integrations.md`: правило исключения non-credential имён и требование
  broker-контракта для Required кредов.

## Verification

- `go test ./internal/toolhub -run 'TestParseExampleEnv|TestRecipeResolver'`
- `just check`
- Ребилд `hermes-hub:0.3.0-dev`, пересоздание стека, повторный self-install.

## Rollback

Удалить фильтр и файл контракта из `contracts_dir`; stored definitions не
затрагиваются.
