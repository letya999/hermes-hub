---
description: "Исполняемые offline gates и отдельная обязательная проверка production-релиза."
last_verified: "2026-09-18"
---

# Пороги качества

| Команда | Проверяет |
| --- | --- |
| just check | gofmt, go vet, architecture imports, secret hygiene, docs/spec/API checks, race+85% coverage, build |
| just test-unit | Pure/domain boundaries и локальные filesystem/crypto adapters |
| just test-integration | Broker/provider/HTTP/composition/CLI компоненты с локальными зависимостями |
| just test-e2e | Standalone app + TCP HTTP + browser + SDK + runtime boundary |
| just fuzz | Три короткие fuzz campaigns identity, JSON, paths; 3 секунды на target |
| just lint-security | Поддерживаемая версия Go и установленные staticcheck/gosec/govulncheck |
| just release-check | Все offline gates, fuzz и внешние security tools; отсутствующий инструмент — ошибка |
| just docs-fix | Генерация индексов, без переписывания frozen specs |

Coverage metric — Go statements, не branches. Общий порог 85%; отдельно каждый production package
должен иметь 85% и выше. Пакеты только с DTO или tests не имеют statements и не создают фиктивный denominator.
`-coverpkg=./...` даёт повторяющиеся блоки: checker объединяет их по source location перед подсчётом.
Не допускаются exclusions для CLI, error paths или adapters.

Secret hygiene сканирует ожидаемые имена файлов, key blocks и GitHub token patterns. Это ограниченный
репозиторный safeguard, не полноценный DLP/Git-history scanner. Дополнительный корпоративный scanner
может вызываться отдельным just recipe. Не изображать запуск отсутствующего scanner.

[Verification](../../.work/done/CHG-001-credential-broker/artifacts/verification.md) — фактическое evidence
данной поставки; этот документ описывает правила, а не результаты конкретного запуска.
