---
description: "credential-broker — единственный компонент, держащий значения секретов: TLS API, sealed store, provider plugins, delivery contracts."
last_verified: "2026-10-20"
---

# credential-broker

`services/credential-broker` — отдельный Go-модуль
(`github.com/letya999/credential-broker`), подключённый к репозиторию как
`replace`-зависимость. Entrypoint: `credential-broker serve --config
/var/lib/credential-broker/config.json`, TLS endpoint
`https://credential-broker:8787` (host `127.0.0.1:8787`).

ADR-0022 (поправка 2026-10-19): Broker — единственный орган власти над секретами
для connector/CLI путей. Исключения: локальный credstore остаётся подложкой
хранения и входом для uncontracted credential-gate admissions, а unmanaged
deployment хранит `secrets.<env>.env` через runtime self-env (ADR-0049 говорит
про intake-каналы, не про единственное хранилище). ToolHub хранит
references (`broker_credential_id`, `broker_grant_id`, `broker_contract_id`);
runtime получает materialized secrets через broker-controlled delivery или
том `/run/broker-materialized` — никогда через ToolHub, compose env или audit
ledger. «Тела запросов и URL не логируются» — свойство сервиса.

Команды: `init --dir` (development-установка; signing keys принадлежат
доверенному Hub, не MCP-контейнеру), `serve --config`, `check-config`,
`version`. Каждый клиент получает `HUB_CREDENTIAL_BROKER_<ROLE>_{URL,
KEY_FILE,KEY_ID,ISSUER,CA_FILE}` с per-service ключом в named volume
`broker-secrets-<service>` на `/run/broker-secrets`; роли `RUNTIME`
(`hermes-runtime-adapter`), `CONTROL` (`hermes-toolhub`), `APPROVE`
(`hermes-communication`), mutual TLS с pinned CA `server.crt`.

Module internals: `internal/seal` (sealed-at-rest store), `internal/journal`,
`internal/safenet` + `internal/securefs`, `internal/strictjson`,
`internal/archtest`. Новый provider — `provider.Provider` + contract tests;
новый delivery path — декларативный contract + materializer + negative tests.

Quality bar (см. также AGENTS.md в корне, раздел Credential Broker): `just
check`/`test-*`/`docs-check`/`docs-fix`/`release-check`; реализации —
`scripts/quality.py`, `docs_scripts/check.py`; hash-frozen spec в
`specs/baseline.json`. 85% coverage суммарно и per production package;
секрет — никогда в ledger DTO, tool results, argv; broker не container
runner, не identity provider, не remote-MCP installer, не canonical secret
store.

| Документ | Когда открывать |
| --- | --- |
| [Архитектурные решения](../../adr/index.md) | ADR-0060..0069 — импортированные broker ADR |
| [Архитектура](architecture/index.md) | Навигация: архитектура. |
| [Правила документации](documentation/index.md) | Навигация: правила документации. |
| [Инженерные правила](engineering/index.md) | Навигация: инженерные правила. |
| [Эксплуатация](operations/index.md) | Навигация: эксплуатация. |
| [Продукт](product/index.md) | Навигация: продукт. |
| [Спецификации](../../../specs/index.md) | SPEC-0064..0066 — frozen broker-контракты |
