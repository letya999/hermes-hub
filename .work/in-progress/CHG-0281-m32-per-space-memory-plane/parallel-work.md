# M32: небольшие назначения от общей базы

Milestone: [M32 MVP](https://github.com/letya999/hermes-hub/milestone/43).
GitHub — текущий статус issues; CHG — evidence; github-issues.json — карта ID.
Текущие изменения ещё не интегрированы в `dev`. Пути ниже — назначения,
а не уже созданные worktree.

## Что делать сейчас

1. Отобрать текущие изменения M32 в исходной ветке, исключить локальные
   артефакты, создать проверенный commit и влить его в `dev`. Записать
   исходный и итоговый SHA, результаты `just check` и применимых Docker gates.
   Если проверка не прошла, исправить её конкретную причину до разделения.
2. От одного итогового `dev` SHA три отдельных агента получают по одной issue:
   [config #509](prompt-config.md), [memory #513](prompt-memory.md),
   [automation #521](prompt-automation.md).
3. Каждый создаёт свой worktree и ветку от этого `dev` SHA. Один PR — одна
   issue; один физический worktree переиспользуется для следующих issues своей
   lane после интеграции предыдущего PR. Интегратор последовательно объединяет
   проверенные изменения в `dev`; следующая ветка начинается от свежего `dev`.
4. #526/#527 — отдельная интеграционная приёмка после функциональных задач.
   Ни один промежуточный PR не означает готовность MVP.

## Первая волна

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
| [#522](https://github.com/letya999/hermes-hub/issues/522) | A2: полный ToolHub host без production startup | automation | 521, 514 | 0099 |
| [#523](https://github.com/letya999/hermes-hub/issues/523) | A3: единый owner cutover | automation | 522 | 0099 |
| [#524](https://github.com/letya999/hermes-hub/issues/524) | A4: protected daily routine | automation | 523, 516 | 0099 |
| [#525](https://github.com/letya999/hermes-hub/issues/525) | A5: dreaming execution/commit | automation | 524, 518 | 0099 |
| [#526](https://github.com/letya999/hermes-hub/issues/526) | два пользователя и сбои в контейнерах | integration | 508–525 | 0097–0099 |
| [#527](https://github.com/letya999/hermes-hub/issues/527) | финальный cutover и release gates | integration | 526 | 0097–0099 |

Предпочтительный порядок config: #509 → #510 → #511 → #512 → #508.
Это порядок одной lane; реальные обязательные зависимости указаны в таблице.
Memory: #513 → #514 → #515/#516 → #517/#518 → #519 → #520.
Automation: #521 → #522 (ждёт #514) → #523 → #524 → #525.
Когда lane ждёт зависимость, не расширять её задание автоматически.

## Правила интеграции

- Каждый меняет callers/tests своей issue; shared server.go/Compose/catalog/docs
  редактируются минимальными hunks. Нет отдельного «foundation интерфейсов».
  #509 владеет scope admission; #513 — startup vault binding;
  #521 — routines/communication lock boundary. В первой волне #521 не трогает
  ToolHub startup, #513 не трогает scope admission.
- #522 готовит исполнимый host и проверяет его через harness; production startup
  остаётся прежним до #523. Нет второй state copy, compatibility flag или clock.
- Shared-file conflict разрешает интегратор; после свежей базы повторить проверки
  изменившихся гарантий. Не брать helper из незавершённой чужой ветки.
- Catalog bump и tool docs идут с соответствующим contract change, а не в конце
  milestone. Финальный #527 проверяет полноту. Versions выбирать на свежей базе.
- Не выполнять одновременно Docker build/gate/cleanup на общем daemon. Только
  synthetic users, собственный Compose project; без широкого prune.
- Реальные команды/exit codes/SHA и границы evidence обязательны. Stub transport
  не доказывает integration. just check и gates AGENTS.md сохраняются.
- Push своей ветки и draft PR разрешены; merge, закрытие issue, provider mutation,
  production deploy и внешняя отправка в назначения workers не входят.
- Блокер — конкретный недостающий контракт/исправление, не повод брать следующие
  issues без назначения. Непроверенные acceptance пункты остаются pending.

Предыдущие execution-plan.md, handoff, prompt-foundation.md, SPEC-0096 и аудиты —
датированная история. Отдельного foundation или integration branch для M32 нет.
Текущие рабочие контракты — SPEC-0097/0098/0099.

