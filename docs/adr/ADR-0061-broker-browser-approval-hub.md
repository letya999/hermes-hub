---
description: "Подтверждение браузера через Communication Hub."
last_verified: "2026-11-15"
---

# ADR-0061 (imported from services/credential-broker ADR-0002): Подтверждение браузера через Communication Hub

Status: accepted (imported from services/credential-broker).

**Traceability:** [SPEC-0064](../../specs/active/SPEC-0064-credential-broker-v1.md), broker module `services/credential-broker`.

## Контекст

Ссылка из Telegram может утечь; URL не доказывает principal.

## Варианты

Только bearer-link; отдельная user DB; approved browser через существующую identity.

## Решение

Ed25519 role-specific assertions плюс browser pairing code, подтверждаемый Communication Hub.

## Последствия

На один дополнительный шаг больше, но другой браузер нельзя привязать одной украденной ссылкой.

## Когда пересматривать

При наличии проверенной SSO browser session, которую Hub может безопасно связать с request.
