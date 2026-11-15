---
description: "Несколько кредов на connection: consumer_id дискриминатор"
last_verified: "2026-09-28"
---

# Несколько кредов на connection: consumer_id дискриминатор

## Цель

Снять инвариант «ровно один active credential на `(context, connection)`» так, чтобы одна
capability хранила несколько ключей для разных потребителей — несколько SSH-ключей под host alias,
отдельные креды под разные MCP/CLI — и помнила, к какому потребителю каждый кред относится.

## Шаги

- `api/v1`: опциональное `consumer_id` на `CreateRequest`, `Request`, `Credential`, `Event`.
- `broker/enrollment.go`: canonical-валидация consumer, дедуп по `(context, connection, consumer)`
  для credentials и pending requests, rotation не сменяет consumer.
- События `request.created/canceled`, `credential.ready/revoked` несут `consumer_id`.
- ADR-0007, domain-model, user-flows, regenerated `api/openapi.json`.
- Отрицательные тесты: дубликат consumer → conflict, невалидный consumer → invalid,
  rotate с чужим consumer → denied.

## Приёмка

`TestConsumerIDDiscriminator` и существующие enrollment/rotation тесты зелёные в Linux-окружении
(golang container — broker собирается только под linux). OpenAPI drift check и docs-check зелёные.
Полный `just check` на Linux-раннере — отдельный гейт, на Windows-хосте не запускался.
