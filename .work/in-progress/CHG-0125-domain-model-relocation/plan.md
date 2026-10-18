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
   gateway, а gateway не перечислял провайдеров. Начало положено:
   `internal/commops` и `internal/serviceauth` уже несут parser-based
   boundary-тесты (leaf-направление зафиксировано).

## Порядок и критерии

## Статус

- [x] Шаг 1 `internal/commodel`: Job/Delivery/DeliveryButton/Task/User/
  SlackLink/RoutineOccurrence/Schedule+сателлиты/RoutineDraft/JobMapping/
  RunOutcome/PendingControl — методы экспортированы (`Envelope`,
  `SlackEnvelope`, `MockEnvelope`, `Key`, `Schedule`, `JobFingerprint`,
  `MappingFromJob`), communication держит `type X = commodel.X` алиасы.
- [x] Шаг 2 `internal/channel`: Channel iface + Caps + контрактные ошибки
  (`ErrUncertain`, `ErrDeliveryRetried`, `ErrRoutineDenied`,
  `FormatMarkdown`, `TargetVerifier`) + адаптеры telegram/slack/mock за
  Deps-швами (`TelegramDeps`, `SlackDeps`, `MockDeps`, lazy `API func()` —
  nil-gateway pure-методы сохранены). Throttle/requeue/compose остались в
  gateway (`sendEditReply`, `sendParts`, `voicePlan` …) — они gateway-
  семантика, не канальная.
- [x] Boundary-тесты: channel и commodel не импортируют services.
- [x] Шаг 3a `internal/routines` semantics: ScheduleIntent/ValidateSchedule(Draft)/
  NormalizeScheduleDraft/ResolveRelativeReminder + NextSchedule/NextExpression/
  NextCron + ExtractValue/ValidateExtractStep/ApplyExtractStep/WatchValue +
  ExpandRecipe + OccurrenceID/RoutineConversation/RoutineFailureDigest +
  DefaultCatchUp + DraftHooks{Channel,Script} (канал-реестр и Starlark
  остаются у владельца — инжектятся). commodel.IDPattern/ServiceNamePattern.
  ID-золотники фиксируют durable-форматы.
- [ ] Шаг 3b `internal/routines` engine (TickSchedules/occurrences/proactive/
  watchers/digests ~4k строк, Spool-методы) — следующий CHG (Deps-шов к
  spool/runtime/каналам по образцу commops).

- Каждый шаг — отдельный коммит с `just check` PASS и тем же покрытием.
- Никаких дублирующих типов (`commodel.Delivery` vs `channel.Delivery`) —
  только алиасы, единый источник.
- Conformance-тесты канала (`channeltest`) переносят seam-тесты из
  `channel_registry_test.go` в пакетный формат.

## Out of scope

- Новые каналы, web-auth, spool-migration (Ф4), differential replay.
