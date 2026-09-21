---
description: "Маршруты чтения, проверяемые правила и команды для агента или разработчика."
last_verified: "2026-09-18"
---

# Инструкции для работы над Credential Broker

Сначала читать [scope](docs/product/scope.md), [границы слоёв](docs/engineering/repository-structure.md),
[спецификацию](specs/active/credential-broker-v1.md) и [SECURITY](SECURITY.md).
Меняется текущий код — обновить `docs/`; изменяется решение — добавить ADR, не переписывать историю.

## Команды

Пользователь и CI запускают только `just`: `check`, `test-unit`, `test-integration`, `test-e2e`,
`test-race`, `fuzz`, `docs-check`, `docs-fix`, `release-check`. Реальные реализации этих команд в
`scripts/quality.py` и `docs_scripts/check.py`, без скрытого второго набора флагов.

## Непереступаемые границы

Не хранить секрет в ledger DTO, tool results, аргументах процесса или примерах. Не выключать TLS,
Origin/CSRF, аудит или авторизацию ради совместимости. Не превращать Broker в container runner,
identity provider, remote-MCP installer или canonical secret store.

Новый provider — реализация `provider.Provider` и contract-тесты. Новый способ доставки —
декларативный contract, материализатор, отрицательные tests и решение по trust boundary.
Любое расширение shared execution сначала проверяется на смешение identities и process-global state.

## Документация

Документация на русском. Frontmatter содержит ровно quoted `description` и `last_verified`.
`index.md` состоит из frontmatter, H1 и генерируемой аннотированной таблицы. Править через `just docs-fix`.
`specs/active` заморожены; их hashes проверяются. Следующая версия требования — новый spec и ADR.
Прогресс, реальные команды и замечания review живут только в `.work`, не в текущих architecture docs.

Открывать изменение: `just change-new change-name 'Название изменения'`. Значимые шаги фиксировать
в `plan.md` и `state.yaml`; мелкие очевидные правки не требуют отдельной бюрократии.
`state.yaml` использует JSON-совместимое подмножество YAML 1.2 — без дополнительной Python-зависимости.

## Качество

85% покрытия суммарно и каждого production-пакета — нижняя граница, не цель вместо meaningful tests.
Не исключать файлы из denominator. Команды, которые не запускались, отмечать как не запущенные.
Не утверждать live E2E по локальному TLS simulator. Не обещать «универсально» там, где нужен adapter.
