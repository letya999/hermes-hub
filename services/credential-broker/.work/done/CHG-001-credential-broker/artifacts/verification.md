---
description: "Отчёт поставки: фактически выполненные проверки Credential Broker и явно незапущенные."
last_verified: "2026-09-21"
---

# Отчёт поставки

## Выполнено в монорепозитории

- `just credential-broker-check` (`python3 scripts/quality.py check`) в CI: gofmt -l,
  `go vet ./...`, OpenAPI drift check, `go test ./internal/archtest -count=1`,
  secret hygiene scan и docs check.
- `go test -race -shuffle=on -count=1 ./...` корневого модуля hermes-hub; пакеты
  broker-модуля в этот прогон не входят (отдельный Go module).
- `docs_scripts/check.py check`: метаданные, индексы, ссылки, спецификации, state-схемы.

## Не запускалось в этой поставке

- `just release-check`, `test-integration`, `test-e2e`, `test-race`, `fuzz` broker-модуля.
- Живые contract-тесты с Vault, Kubernetes, Google и readiness MCP.
- Независимый сторонний аудит; образы и бинарники для production не собирались.

## Покрытие

Гейт 85% суммарно и по каждому production-пакету описан в правилах качества.
Числа последнего зафиксированного прогона приводятся в журнале CI, здесь не дублируются.
