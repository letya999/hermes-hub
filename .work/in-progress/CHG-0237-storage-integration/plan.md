# CHG-0237: Интеграция Storage reliability MVP в dev

Рабочая ветка: `chore/storage-reliability-integration`, создана от `dev` на
`ba5edcb9` (2026-10-31). Источник: `codex/storage-reliability-mvp`, коммит
`0a8cbf7e` плюс незакоммиченные изменения. Цель этого CHG — подготовить и
проверить перенос, затем интегрировать только фактически готовые изменения.
Этот план сам по себе не является merge, миграцией данных или приёмкой.

## Что уже есть в источнике

| Область | Состояние на 2026-10-31 | Что приносит |
|---|---|---|
| Broker recovery (старый CHG-0191) | Код и тесты, приёмка ожидается | Версионированная заверенная «текущая голова» журнала, проверка потери primary/recovery, явное принятие legacy head. |
| Event state (0192) | Код и тесты, приёмка ожидается | Разделение курсоров, run events и provider dedup; строгая миграция старой раскладки и защита живых курсоров от очистки. |
| Diagnostics (0193) | Код и тесты, приёмка ожидается | Ротация без truncate, ограниченные чтения, scoped trace, явные пропуски и безопасная проекция полей. |
| Control audit (0197) | Код и тесты, приёмка ожидается | Конечные сегменты, durable admission/completion, различение отказа, ошибки после эффекта и неизвестного исхода. |
| Spool handoff (0198) | Код и тесты, приёмка ожидается | Канонический receipt результата/доставки, повтор source batch с теми же ID, запрет слепого повтора неопределённого эффекта. |
| Storage budget (0199) | Частично реализован | Учёт и обслуживание Communication/ToolHub, `hubctl storage` status и dry-run; Broker часть зависит от будущего SQLCipher. |
| ToolHub state/confirmation (0200) | Частично реализован | Синхронная запись snapshot и защита initialized store; подтверждение человеком через Telegram callback. Другие каналы и ряд проверок ещё не завершены. |
| Runtime reconciliation (0201) | Частично реализован | Сохраняемые desired/applied revision и restart cause, digest поверхности инструментов, отдельные причины model outage. |
| SQLCipher proof/store, object GC, migration (0194–0196, 0202) | Только проект | Native failure gate, новая DB, lifecycle secret objects и миграция рабочих данных **не реализованы**. |

Broker остаётся отдельным Go модулем. SQLCipher предназначен только ему; root
ToolHub и Communication сохраняют существующие JSON/spool механизмы. Секретные
значения не попадают в ledger, audit, argv и диагностику. Внешние эффекты и
развёртывание не входят в этот merge-план.

## Расхождение с dev и документами

Коммит источника `0a8cbf7e` уже предок `dev`: обычный merge текущего ref ничего
не внесёт. В worktree источника на момент анализа были 204 изменённых
отслеживаемых файла и 96 новых. С момента общей базы `dev` изменил 543 пути;
74 пути изменены обеими сторонами. Временный Git snapshot через отдельный index
и `git merge-tree` показал 26 текстовых конфликтов; исходный index, рабочее
дерево и ref не менялись. Новые пути не столкнулись по имени, но часть их
содержимого дублирует уже перенумерованные записи `dev`.

Канонические номера в `dev`:

| Источник | Уже в dev |
|---|---|
| ADR-0084 / ADR-0085 | ADR-0087 / ADR-0088 |
| SPEC-0084 / SPEC-0085 | SPEC-0087 / SPEC-0088 |
| CHG-0191…0202 | CHG-0215…0226 (прибавить 24) |

Не переносить старые ADR/SPEC/CHG как дополнительные файлы: номера 0084/0085
и 0191…0202 в `dev` заняты другими решениями. Принятые ADR и frozen SPEC не
переписывать; результаты реализации и gate receipts перенести в существующие
CHG-0215…0226, затем обновить current docs по проверенному поведению.

## Конфликты и решение

| Группа | Файлы с текстовым конфликтом | Стратегия |
|---|---|---|
| Учёт и документация | `.work/index.md`, `docs/adr/index.md`, `specs/index.md`, `docs/operations.md`, `docs/routines.md`, `docs/tools.md`, пять `docs/services/{communication,model-relay,runtime,supervisor,toolhub}.md` | Оставить номера, уже принятые в `dev`; перенести только новые проверенные факты. Сохранить новые описания M24/M26, локализацию и live tool inventory. Проверить ссылки и `last_verified`. |
| Каталог инструментов | `internal/agenttools/capability.go`, `routine_compact_test.go` | Взять полный каталог `dev` 5.9.0 (включая навыки и `terminal.exec skill`), добавить новые audit/diagnostics формулировки. Выпустить следующий свободный immutable version; одновременно обновить `prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, тесты. После развёртывания нужны rebind и respawn managed spaces. |
| Communication | `budget.go`, `ceilings_test.go`, `events.go`, `gateway.go`, `media.go`, `spool.go`, `internal/routines/engine.go` | Сохранить параллельные conversation lanes, суточные budget counters, watcher flow и локализованные ответы `dev`; добавить event layout migration, durable receipt/batch и учёт хранения. Перед переносом проверить все callers изменённых сигнатур и регрессии crash/replay, isolation и concurrency. |
| Авторизация и HTTP | `internal/toolhub/control_credentials.go`, `control_http.go`, `internal/secrets/secrets.go`, `internal/supervisor/execute.go` | Совместить авторизацию формы и trusted callback без agent self-confirm; связать actor/scope/digest/revision/expiry. Не включать незавершённый путь для других каналов. Сохранить HTTP error contract и `locale.T` для всех пользовательских строк; в источнике есть жёстко записанный русский текст. Для post-effect ошибок возвращать uncertain без повторной выдачи/отправки. |
| Тесты стека | `internal/stack/config_test.go`, `prompt_contract_test.go` | Сохранить проверки навыков, актуальных образов и live tool inventory; добавить новый contract/restart coverage без ослабления assertions. |

Автоматически слитые 48 пересекающихся файлов тоже требуют смыслового review:
особенно `internal/runtime`, `internal/toolhub`, `internal/communication` и
`internal/stack`. Git не обнаруживает потерю более нового контракта, если строки
не пересеклись. Источник предшествует изменениям `dev` для локализации,
M24 skills и parallel lanes; выбор целого файла с одной стороны недопустим.

## Порядок интеграции

1. Заморозить и повторно проверить dirty source; отделить данные/артефакты от
   кода, пройти secret scan и `git diff --check`. Сохранить WIP в логических
   локальных коммитах источника без сброса чужих правок. Повторить `merge-tree`
   на точном коммите; эта оценка конфликтов устареет при новых правках.
2. Слить источник в `chore/storage-reliability-integration` с сохранением двух
   родителей. Удалить из результата дубли старых ADR/SPEC/CHG, перенести
   evidence/state в канонические CHG-0215…0226. Ничего не вливать прямо в `dev`.
3. По очереди совместить Broker head, event layout, diagnostics, audit и handoff.
   Для каждого проверить callers, локальные fault/replay/isolation тесты и
   текущие правила безопасности; не совмещать по принципу «взять ours/theirs».
4. Отдельно закончить ToolHub confirmation, storage budget и runtime
   reconciliation. Неполные SQLCipher/GC/migration этапы оставить как планы,
   без включения нового формата и без изменения рабочих volumes.
5. В одном changeset довести agent-tool contract и четыре каталога локализации.
   Проверить managed/team grants, доступность навыков, стабильные tool names и
   отказ в неподдерживаемых каналах.
6. Запустить `just check` (root coverage >=85%), Broker module pipeline
   (>=85% overall и по production package), `just security` только при
   изменении зависимостей. Затем isolated native/container fault gates;
   image-side правки проверять через разрешённые `hubctl`/`just` пути.
   Синтетические тесты не считать live интеграцией.
7. Перед переносом в `dev` сверить diff с текущим `dev`, убрать незавершённые
   заявления в документации, зафиксировать реальные receipts и список
   оставшихся gate. Реальные данные мигрировать только отдельным явным
   операторским шагом CHG-0226 с проверенным backup и планом отката вперёд.

## Критерий готовности этого плана

Ветка интеграции создана, конфликтная поверхность измерена, старые и новые
номера сопоставлены, неполные части отделены от готового к переносу кода.
Фактический merge, сборка и приёмка остаются следующими задачами.
