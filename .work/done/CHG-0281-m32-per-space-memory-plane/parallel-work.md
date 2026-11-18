# M32: три постоянных трека, продолжение в Devin

Актуализировано 2026-11-06 по решению владельца. Этот план заменяет старые
назначения первой волны и требования создавать новую ветку после каждой issue.
Milestone: [M32 MVP](https://github.com/letya999/hermes-hub/milestone/43).

## Текущее состояние

Общая база dev — 6eae50d4. Три worktree уже существуют; сохранить их ветки и dirty diff.
Все три checkout чистые после checkpoint 06.11 около 10:06 МСК.
Снимок коммитов ниже не означает закрытие issue или итоговую интеграционную приёмку.

| Трек | Worktree | Зафиксировано | Следующее |
|---|---|---|---|
| config | C:/Users/User/a_projects/hermes-hub-wt/m32-track-1 | #509–#512, checkpoint #508: 33fb159d | #508 |
| memory | C:/Users/User/a_projects/hermes-hub-wt/m32-track-2 | #513–#516, checkpoint #517: 50e6bb0c | #517–#520 |
| automation | C:/Users/User/a_projects/hermes-hub-wt/m32-track-3 | #521–#522, checkpoint #523: 813a37d6 | #523–#527 |

Перед передачей проверить свежие status/HEAD и остановить прежнего писателя в этом
worktree. Не запускать Devin и Codex одновременно над одним diff.
Не создавать ветки с префиксом codex/. Существующие ветки не переименовывать.

## Как продолжать

Полные последовательные этапы и результаты:
[Devin handoff](../CHG-0289-fast-local-verification/devin-handoff.md).
Короткие входы: [config](prompt-config.md), [memory](prompt-memory.md),
[automation](prompt-automation.md).

Один worktree и одна ветка на трек; небольшие этапы внутри issue. Коммит issue —
после завершения её связного контракта. Не объявлять промежуточный owner cutover
готовым к запуску. Новые сервисы, временные заглушки и legacy fallback не нужны.

Обязательное чтение: AGENTS/README/docs index, текущая issue, инварианты и её строка
SPEC-0097/0098/0099. Исторические ADR/SPEC — по конкретному противоречию.

## Зависимости

Исправление порядка выполнения, принятое владельцем: #522 не требует memory-only
admission #514 для реализации automation host. Сам контракт #514 сохраняется для
memory-вызовов. Старое ожидание всей ветки памяти до #520 отменено: интегрировать
проверенные коммиты, реально необходимые #524/#525. Незакоммиченные файлы не копировать.

| Issue | Branch | Worktree |
|---|---|---|
| #509 | codex/m32-509-tool-scopes | C:/Users/User/a_projects/hermes-hub-wt/m32-509-config |
| #513 | codex/m32-513-vault-backend | C:/Users/User/a_projects/hermes-hub-wt/m32-513-memory |
| #521 | codex/m32-521-engine-boundary | C:/Users/User/a_projects/hermes-hub-wt/m32-521-automation |

#509 уже имеет PathScopes/NarrowSelectionScopes: завершить/проверить, не переписать.
#513 подключает production backend, но не открывает model API без #514.
#521 сохраняет текущего единственного владельца, отделяя engine от spool.
Публичное переключение всех automation paths происходит только в #523.

## Актуальная очередь

Каждая issue требует интегрированной общей базы в `dev`. Зависимости ниже —
только implementation issues.
Обязательное чтение работника: AGENTS/README/docs index, одна issue и указанная
короткая SPEC (инварианты + строка своей issue). Исторические ADR/SPEC — справка
при конкретном противоречии; ADR-0097 объясняет новое разбиение.

| Issue | Результат | Lane | Depends on | SPEC |
|---|---|---|---|---|
| [#508](https://github.com/letya999/hermes-hub/issues/508) | C5: settings через publisher | config | 511, 512 | 0097 |
| [#509](https://github.com/letya999/hermes-hub/issues/509) | C1: завершить authored scopes | config | — | 0097 |
| [#510](https://github.com/letya999/hermes-hub/issues/510) | C2: connector/raw-MCP selection | config | — | 0097 |
| [#511](https://github.com/letya999/hermes-hub/issues/511) | C3: согласованная активация | config | — | 0097 |
| [#512](https://github.com/letya999/hermes-hub/issues/512) | C4: crash recovery и revoke | config | 511 | 0097 |
| [#513](https://github.com/letya999/hermes-hub/issues/513) | M1: per-user vault backend | memory | — | 0098 |
| [#514](https://github.com/letya999/hermes-hub/issues/514) | M2: exact-run admission | memory | — | 0098 |
| [#515](https://github.com/letya999/hermes-hub/issues/515) | M3: memory_search | memory | 513, 514 | 0098 |
| [#516](https://github.com/letya999/hermes-hub/issues/516) | M4: durable trusted capture | memory | 513, 514 | 0098 |
| [#517](https://github.com/letya999/hermes-hub/issues/517) | M5: recall/CORE, убрать shared scope | memory | 515, 516 | 0098 |
| [#518](https://github.com/letya999/hermes-hub/issues/518) | M6: remember/card writer | memory | 515, 516 | 0098 |
| [#519](https://github.com/letya999/hermes-hub/issues/519) | M7: confirmed edit/CAS/single use | memory | 518 | 0098 |
| [#520](https://github.com/letya999/hermes-hub/issues/520) | M8: governed OpenKnowledge | memory | 517, 519 | 0098 |
| [#521](https://github.com/letya999/hermes-hub/issues/521) | A1: отделить engine от spool lock | automation | — | 0099 |
| [#522](https://github.com/letya999/hermes-hub/issues/522) | A2: полный ToolHub host без production startup | automation | 521 | 0099 |
| [#523](https://github.com/letya999/hermes-hub/issues/523) | A3: единый owner cutover | automation | 522 | 0099 |
| [#524](https://github.com/letya999/hermes-hub/issues/524) | A4: protected daily routine | automation | 523, 516 | 0099 |
| [#525](https://github.com/letya999/hermes-hub/issues/525) | A5: dreaming execution/commit | automation | 524, 518 | 0099 |
| [#526](https://github.com/letya999/hermes-hub/issues/526) | два пользователя и сбои в контейнерах | integration | 508–525 | 0097–0099 |
| [#527](https://github.com/letya999/hermes-hub/issues/527) | финальный cutover и release gates | integration | 526 | 0097–0099 |


## Проверки и интеграция

- Применить CHG-0289 после его проверки/коммита. Пока он не попал в worktree,
  не считать, что локальный justfile автоматически обновился.
- `just check`: format/docs/vet; `just test <packages>`: затронутые пакеты и callers.
  Для изменения конкурентности — адресный race. Probity оставить включённым.
- Полный race/coverage/security и контейнеры — CI с точным SHA и ссылкой на run.
  Один repo-wide локальный прогон за раз; Docker общего dev stack не трогать.
- Только трек 3 координирует интеграцию готовых коммитов. Проверять реальные
  зависимости; не ждать произвольный конец соседнего трека и не делать фиктивные merge.
- Для запуска CI сначала нужен опубликованный код. Push/PR/GHCR publication —
  отдельная инструкция владельца; отсутствие такой инструкции фиксировать как
  незавершённую удалённую приёмку, а не эмулировать весь CI локально.
- Capacity/429/ошибка запуска процесса — немедленный конкретный blocker с
  последним завершённым этапом, а не часы видимой «работы» без прогресса.
