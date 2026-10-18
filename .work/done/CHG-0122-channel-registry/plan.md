---
id: CHG-0122
title: "Channel registry — delivery and authorization dispatch polymorphically"
type: refactor
scope: internal/communication
status: done
---

# CHG-0122 — Phase 2: канал-агностик контракт (delivery + auth seams)

Цель Ф2: механики хаба работают через канал-нейтральные абстракции,
провайдер подключается регистрацией, а не разрастанием switch-ей.

## Срезы

### S1. Channel interface + delivery dispatch

`Channel { ID(); Deliver(ctx, *Delivery) error; AuthorizeOccurrence(job, user, configured) error; ValidateOccurrence(o, caller) error }`.
Реализации — `channel_telegram.go`, `channel_slack.go`, `channel_mock.go`,
по одному файлу на провайдера. Реестр `g.channels` (sync.Once — lazy для
struct-literal фикстур) собирается из New(): telegram всегда, slack при
SlackBotToken, mock при MockDir.

- `deliverOne` `switch delivery.Channel` → `g.channel(id).Deliver`.
  Durable-запись с неизвестным каналом (записанная в обход
  normalizeDelivery) больше не отправляется через telegram default —
  uncertain + диагностика.
- `authorizeOccurrence`/`validateOccurrence` `switch job.Channel` →
  `channel.AuthorizeOccurrence`/`ValidateOccurrence`. Общие проверки
  (user enabled/org/scope/trigger) остаются в authorizeOccurrence.
- `normalizeDelivery` per-channel identity rules →
  `channel.NormalizeDelivery(*Delivery) error` — тот же интерфейс.

### S2. Inbound seam (минимум)

Poll/push вход уже нормализует в Job — документируем контракт без
перестройки: telegram поллинг остаётся `pollUpdates`, slack/mock — HTTP
роуты. `enqueueChannelJob` теперь сам канал-агностик: envelope и typing
берутся из `JobEnvelope`/`Caps().Typing`. Новый канал добавляется
регистрацией Channel + свой inbound адаптер.

## Итоговая граница

`Channel` = 11 методов: NormalizeDelivery, Deliver, DeliverArtifact,
SendPresence, JobEnvelope, PrepareRoutine, RouteBlockedNotice,
AuthorizeOccurrence, ValidateOccurrence, ValidateSchedule, Caps.
Плюс опциональный `deliveryTargetVerifier`. Реализации:
`channel_{telegram,slack,mock}.go`, реестр `channelPlugins`.

Оставшиеся `"telegram_bot"` литералы — намеренные: default-channel policy
(normalizeDelivery, http.go, RoutineDraft), telegram-скопленные helpers
(queueDelivery, task binds), inbound-адаптеры и destination-инварианты
(telegram task-model: envelope/TelegramIDs/topic binding).

## Out of scope

- Новых каналов нет; Telegram остаётся единственным production.
- Canary/health extraction (Ф1-хвост): перенос в ops-пакет упирается в
  import cycle — ops нужны Spool/User/Delivery из communication, а `/status`
  и `Gateway.Run` вызывают их обратно. Честная граница = deps-интерфейс
  + оркестровка loop'а на уровне cmd — отдельный CHG.
- Web-channel authentication — deferred.
