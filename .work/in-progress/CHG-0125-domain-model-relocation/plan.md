---
id: CHG-0125
title: "Domain model relocation — enabling internal/channel and internal/routines packages"
type: refactor
scope: internal/communication, internal/channel, internal/routines
status: planned
---

# CHG-0125 — Phase 3 remainder: package moves (roadmap)

Остаток Ф3 из плана v2: реальные пакеты `internal/channel` (контракт +
адаптеры + conformance) и дальше `internal/routines`. Анализ зависимостей
(2026-10-18):

## Блокер

`Channel` интерфейс ссылается на доменную модель communication:
`Delivery`, `Job`, `User` (+`SlackLink`), `RoutineOccurrence`, `Schedule`
(+`WatchProbe`, `RoutineDraft`), `Task`, `JobMapping` (+`RunOutcome`) —
11 структур в 7 файлах. Пакет `internal/channel` не может импортировать
communication (цикл: gateway диспетчеризует через реестр).

## Дизайн

1. **Leaf `internal/commodel`** (или сразу `internal/channel` для
   типов доставки): перенести доменные struct + их методы
   (`User.envelope`, `Schedule` helpers, …) — зависимости почти
   stdlib+identity. `type Delivery = commodel.Delivery` и алиасы в
   communication сохраняют все ~335 сайтов и тесты без правок.
2. **`internal/channel`**: `Channel` iface, `ChannelCaps`, registry,
   адаптеры `channel/{telegram,slack,mock}` с узкими deps-интерфейсами
   (TelegramAPI, voice/artifact/markdown помощники) по образцу commops.
3. **`internal/routines`**: движок (schedules/occurrences/proactive/
   watchers/digests ~3.5k строк) — после появления модели и channel
   пакета; нужен ещё один deps-шов к spool/runtime/каналам.
4. **import-boundary тест**: go list guard, чтобы каналы не тянули
   gateway, а gateway не перечислял провайдеров.

## Порядок и критерии

- Каждый шаг — отдельный коммит с `just check` PASS и тем же покрытием.
- Никаких дублирующих типов (`commodel.Delivery` vs `channel.Delivery`) —
  только алиасы, единый источник.
- Conformance-тесты канала (`channeltest`) переносят seam-тесты из
  `channel_registry_test.go` в пакетный формат.

## Out of scope

- Новые каналы, web-auth, spool-migration (Ф4), differential replay.
