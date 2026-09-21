---
description: "Перенос Credential Broker в Hermes Hub и opt-in интеграция с ToolHub/runtime."
last_verified: "2026-09-19"
---

# CHG-0028 Credential Broker merge

## Цель

Перенести исходники Credential Broker в монорепозиторий Hermes Hub как отдельный
Go-модуль/процесс, сохранить его security boundary и подключить его к существующим
ToolHub, Communication Hub и runtime через opt-in adapter.

## Границы

- Broker остаётся отдельным process/container и владеет credential enrollment,
  grants, leases и runtime materialization.
- ToolHub сохраняет ownership, immutable MCP artifacts, ToolHive admission,
  readiness, projection и Hermes reconnect.
- В первом срезе не мигрируются live credentials и не удаляется текущий credstore.
- Копируются исходники, tests, docs, specs и отдельная MIT-лицензия; служебные
  GitHub/GitLab workflows и чужая история не дублируются внутрь монорепозитория.

## Этапы

1. Скопировать Broker в `services/credential-broker` с отдельным `go.mod`.
2. Добавить root build/test recipes и Dockerfile для Broker.
3. Добавить opt-in stack environment и role-specific signer wiring.
4. Добавить ToolHub control/grant и runtime lease/materialization adapters через
   публичный Broker SDK; Broker mode должен fail closed для старых local refs.
5. Добавить Communication Hub approval command, contract tests и один disposable
   adapter smoke.
6. Добавить dynamic ToolHive read-only file mount hand-off и проверить живой
   Linux Broker instance без provider tokens в репозитории.
7. Обновить docs/spec/ADR и пройти `just check`, `just security`, `just docker-check`
   насколько позволяют доступные локальные инструменты.

## Rollback

Удалить/отключить opt-in Broker setting; текущий ToolHub credential path остаётся
рабочим. Никаких provider tokens, browser profiles, deployment volumes или
generated runtime secrets в репозиторий не добавлять.
