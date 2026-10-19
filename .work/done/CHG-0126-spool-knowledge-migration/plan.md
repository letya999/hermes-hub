---
id: CHG-0126
title: "Spool migration — knowledge moves from comm spool to runtime state on existing deployments"
type: migration
scope: cmd/hubctl
status: planned
---

# CHG-0126 — Phase 5: spool migration (knowledge relocation)

Ф5 из плана v2: офлайн-команда `hubctl spool migrate` под `LockSpool`,
переносящая данные между старым и новым расположением на живых развёртываниях.

## Контекст

CHG-0121 перенёс чтение knowledge из `<spool>/knowledge/<principal>[/context]`
в `<state>/knowledge/<principal>[/context]` и удалил `/v1/knowledge` без
совместимого чтения. На существующих хостах старые файлы остались в spool и
невидимы для agenttools. Единственная релокация данных за весь рефактор Ф1–Ф3:
все прочие spool-каталоги (`schedules`, `occurrences`, `watchers`, `digests`,
`tasks`, `events`, `outbox`, `mappings`, `conversations`, `inbound`,
`callbacks`, `approval-messages`, `canary.jsonl`) читаются по тем же путям —
`git diff dev` по dir-спискам и точкам `Join(s.root, …)` пуст.

## Дизайн

`hubctl spool migrate --spool <dir> --state <dir> [--user <id>] [--apply]`

- Dry-run по умолчанию, `--apply` выполняет — по образцу migrate-spaces/
  migrate-toolhub.
- Выполняется под `communication.LockSpool(spool)` — офлайн, gateway down.
- Сканирует `<spool>/knowledge/<principal>[/context]/<name>`: principal/context
  — `identity.ValidID`, name — `knowledgeName`, только regular files ≤ 9 MiB,
  симлинки и прочие имена → `skipped` с причиной.
- **Копия, не перенос.** Файл пишется в `<state>/knowledge/<principal>/…`
  атомарно (temp+rename, как `writeKnowledge`), источник сохраняется —
  откат на старый образ остаётся возможным. Существующая цель с другим
  содержимым → `conflict`, файл не трогаем; идентичная → `unchanged`.
  Идемпотентно, повторный прогон безопасен.
- Удаление `<spool>/knowledge` — ручной шаг оператора после подтверждения
  релиза (фиксируется в отчёте/доке), не часть команды.

## Slices

### S1. spool migrate команда

Anchors: `cmd/hubctl/main.go` op dispatch; `cmd/hubctl/apply.go`
`writeKnowledge`, `knowledgeName`; `internal/communication` `LockSpool`.

Change:
1. `cmd/hubctl/spool_migrate.go`: `runSpoolMigrate`, `spoolMigrate`,
   `SpoolMigrationReport`.
2. `cmd/hubctl/main.go`: `case "spool"` → подкоманда `migrate`.

Tests:
- `TestSpoolMigrateKnowledge`: фикстура со старым деревом, dry-run ничего
  не пишет, apply копирует (principal + context + conflict + symlink-skip).

## Red-team checklist

- Симлинк/невалидное имя в `spool/knowledge` — пропуск, не чтение вне root.
- Цель уже существует с другим контентом — conflict, нет перезаписи.
- Gateway запущен — LockSpool отказывает, команда честно падает.
- Повторный запуск — только `unchanged`, нулевая запись.

## Acceptance

Dry-run на фикстуре показывает план без записи; apply копирует дерево;
`just check` зелёный.

## Out of scope

- Совместимое чтение старого пути в новом коде — не возвращается (удалено в
  CHG-0121 осознанно; миграция — мост).
- Любые другие каталоги spool — пути не менялись.
- Credential store/audit ledger — поверхность удалена в CHG-0120, данных для
  переноса нет.
