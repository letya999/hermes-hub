---
description: "Request, credential, grant, lease, state и независимые оси области действия."
last_verified: "2026-09-18"
---

# Модель данных и владения

| Сущность | Смысл |
| --- | --- |
| Contract | Проверенный набор fields, deliveries, state и routes; неизменяемая revision |
| Request | Enrollment одного connection/onboarding от canonical Actor, TTL и idempotency |
| Browser session | Hash cookie, hash pairing code, approved/consumed; не учётная запись |
| Credential | Owner user/context, provider ref, revision, active/revoked/reauthorize/deleted |
| Grant | Credential разрешён конкретным principal/context/runtime/binding/workload/policy |
| Lease | Краткоживущий доступ к grant и точной credential revision; по умолчанию 60 секунд |
| Materialized | Plaintext ENV и file mounts только доверенному runtime; не control DTO |
| State | Provider ref snapshots по credential/context/binding; один активный lease-writer |
| Ledger event | Durable sequence и correlation IDs без секретных значений |

OwnerKind `user` разрешает grants только своему principal. OwnerKind `context` требует
`context_manager=true` от trusted identity signer. Пользователь не может сам прислать этот флаг через форму.
Все IDs проходят canonical regex Hermes `[a-z][a-z0-9_-]{0,39}`. Broker IDs используют отдельные
префиксы request/credential/grant/lease, не подменяют исходные canonical IDs.

Grant содержит immutable `policy_version`. Изменённая policy требует нового grant: старый не начинает
автоматически доверять новой политике. Shared workload с process-wide credential не допускает другое
значение для следующего пользователя. Mutable state в shared contracts запрещён целиком.

Удаление импортированного alias credential не удаляет исходный внешний secret. Managed значения
создаются в отдельных provider locations; удаление обходит весь известный список managed revisions.
Сохранённые оператором backups находятся вне этой гарантии.
