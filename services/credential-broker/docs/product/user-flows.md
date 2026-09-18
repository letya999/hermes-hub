---
description: "Связь Telegram, защищённой формы, external account и MCP binding."
last_verified: "2026-09-18"
---

# Пользовательские сценарии

## Личное подключение

Communication Hub подтверждает внешний канал и canonical principal. ToolHub создаёт credential request
с contract revision, connection и onboarding IDs. Broker выдаёт URL. Пользователь открывает браузер,
передаёт pairing code в тот же доверенный личный канал. Approve signer подтверждает конкретный браузер.
После submit/consent Broker сохраняет credential в выбранном provider и публикует событие.
ToolHub автоматически продолжает durable onboarding, а не ждёт ещё одной команды «продолжай».

## Один credential, несколько MCP

Владелец выдаёт каждому binding отдельный grant. Схемы inputs/OAuth должны быть совместимы.
Каждый workload получает свой lease, file directory и при необходимости отдельный mutable state.
Одинаковые credentials не означают общий изменяемый config directory.

## Общий MCP

Контекстный manager создаёт context-owned credential и разрешает named principals через grants.
Shared process с ENV/files может использовать только один credential и один process contract.
Разные личные credentials разрешены в общем workload только для proxy-only contracts, когда trusted
runtime adapter достоверно определяет principal **на каждый запрос**. Иначе ToolHub запускает отдельные процессы.

## Истечение и отзыв

Просроченная/использованная ссылка не открывает новую форму. Отзыв снимает grants/leases доступа;
старый signed assertion не обходит проверку текущего состояния. Ротация меняет revision и требует
нового runtime lease. ToolHub убирает прежнюю tool projection и останавливает или переконфигурирует MCP.
