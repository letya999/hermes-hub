---
id: CHG-0123
title: "Ops plane extraction — canary and health leave communication package internals"
type: refactor
scope: internal/communication, internal/commops
status: in-progress
---

# CHG-0123 — Phase 1 tail: canary/health ops extraction

Ф1 хвост из плана v2: `canary, health → communication/ops`. Прямой сабпакет
`internal/communication/ops` невозможен — ops-коду нужны `Spool`/`User`/
`Delivery` из communication, а `Run` и `/status` вызывают его обратно
(import cycle). Реальная граница без цикла: leaf-пакет
`internal/commops`, который не импортирует communication вообще — все
зависимости объявлены как func-поля `Deps`, провайдер — communication.
Код ops-плоскости не может тронуть Gateway-внутренности сверх Deps.

## Slices

### S1. `internal/commops` — Engine + Deps

`Deps`: Now, SpoolRoot, Owner, BindTask, Enqueue, MappingStatus,
Mappings, UsageTotals, Restarts, SurfaceMissingTools, MockLink,
MockDelivered, NotifyOwner, JobTimeout, DeliveryGrace — только func-поля
и собственные типы (Owner{ID, TelegramIDs}, Mapping, Totals). Никаких
типов communication в пакете — цикл исключён по построению.

Переносятся почти дословно: Interval (HUB_CANARY_INTERVAL), Run
(synthetic mock job → класс отказа), ToolCheck (events/-скан receipt'ов —
локальный минимальный декодер last_event/tool_name), DeliveryCheck
(mock-outbox poll через MockDelivered dep — имя файла не утекает),
Record/journal/state (canary.jsonl + canary-state.json под SpoolRoot,
одно уведомление на класс), Loop, HealthLines (p50/p95, unconfirmed,
failed, restarts, avg tokens).

### S2. Wiring + совместимые шимы

`Gateway.opsEngine()` — sync.Once lazy (struct-literal фикстуры).
Кложуры читают g-поля на момент вызова. Шимы сохраняют все существующие
тесты без правок: `canaryInterval`, `g.runCanary`, `g.recordCanary`,
`g.canaryToolCheck`, `g.canaryLoop`, `g.healthLines`. Пакетные переменные
`canaryJobTimeout`/`canaryDeliveryGrace` остаются в communication и
читаются через deps-функции — тесты их мутируют.

`g.configUser(id)` — helper поиска enabled user по ID (для surface/
restarts/enqueue кложур).

## Out of scope

- Оркестрация loop'а на уровне cmd (Run по-прежнему спавнит canaryLoop —
  процессный контракт не меняется).
- Health: `/status` render остаётся в communication — это его HTTP
  контракт; engine отдаёт строку.

## Acceptance

- `just check` PASS, покрытие ≥85%.
- Все существующие TestCanary*/health тесты зелёные без правок.
- `internal/commops` не импортирует internal/communication.
