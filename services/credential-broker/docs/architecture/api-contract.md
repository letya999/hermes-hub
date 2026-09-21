---
description: "Версия v1, audiences, errors и отделение секретного runtime-канала."
last_verified: "2026-09-18"
---

# HTTP API и identity contract

Машиночитаемое описание: [OpenAPI 3.1](../../api/openapi.json).
Go DTO: [api/v1](../../api/v1/types.go). SDK: [client](../../client/client.go).

## Подпись

`Authorization: Bearer payload.signature` — **не JWT**. Payload — raw URL-safe base64 JSON `identity.Claims`.
Signature — Ed25519 от строки `hermes-credential-broker/assertion/v1.` + payload. JSON содержит version=1,
key_id, issuer, audience, issued_at, expires_at, method, exact request_uri, hex SHA256 body и Actor.
Разница expiry/issued не более 120 секунд; SDK выдаёт 60 секунд. Ключ выбирается только из trusted config.
Сигнатура защищает от подмены тела, метода и пути; она не является одноразовым nonce для каждого HTTP call.
Повторяемые бизнес-операции защищаются своими idempotency keys/state, а proxy writes нельзя автоматически
повторять без гарантии идемпотентности внешнего API.

| Audience | Источник ключа | Разрешённый контур |
| --- | --- | --- |
| broker:control | ToolHub | Contracts, requests, grants, acquire, refs, events, revoke/delete |
| broker:approve | Communication Hub | Подтверждение pairing code конкретного request |
| broker:runtime | Runtime adapter | Inspect/renew/materialize/release lease; restricted API proxy |

`Actor` использует canonical principal/context/runtime/policy; runtime дополнительно обязан передавать
binding/workload. External identity/conversation применяются для корреляции, не вместо ownership check.
SDK вычисляет hash и подпись: не собирать assertions вручную в агентном tool.

## Контрольный поток

`POST /v1/requests` → `authorization_url`; `POST /v1/requests/{id}/approve` через отдельную роль;
пользователь submit/consent; `GET /v1/requests/{id}` или cursor events → credential ID;
`POST /v1/credentials/{id}/grants`; `POST /v1/leases` → descriptor.

`GET /v1/events?after=N` возвращает до 100 видимых событий и `next_cursor`. Cursor глобальный,
поэтому видимые номера могут иметь разрывы. Consumer сохраняет cursor и идемпотентно обрабатывает sequence;
события не заменяют повторную проверку текущей credential/grant revision.

## Runtime

`POST /v1/runtime/leases/{id}/materialize` возвращает ENV и mounts. **Этот ответ содержит секреты.**
Он не должен проходить через Hermes messages, control-job state, общий audit или brokerctl stdout.
`POST .../renew` продлевает в пределах TTL; `POST .../release` принимает checkpoint/quiesced.

Proxy path: `/v1/runtime/proxy/{lease}/{route}/upstream/path?query`. Domain задаёт reviewed route,
не request. Только методы/path prefixes из контракта; каждый call подписывается runtime adapter.
Нет HTTP CONNECT, перенаправлений, SSE и доступа к произвольному URL.

## Ошибки

401 unauthorized; 403 denied; 404 not_found; 400 invalid_request; 409 conflict/reauthorize_required;
410 expired; 413 body_too_large; 429 rate_limited; 503 unavailable.
Внешние response bodies/токены не включаются в error message. Secret validation не заменяет provider ACL.
