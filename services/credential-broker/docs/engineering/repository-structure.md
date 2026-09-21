---
description: "Где менять domain, providers, transport и интеграцию."
last_verified: "2026-09-18"
---

# Структура репозитория и слои

| Каталог | Ответственность |
| --- | --- |
| contract | Reviewed fields/delivery/state/routes, validation/digest/compatibility |
| identity | Canonical Actor и purpose-separated assertion |
| api/v1 | Wire DTO; не содержит бизнес-авторизации |
| provider | Replaceable storage SPI и actual local/Vault/K8s adapters |
| materialize | ENV/files/state без контейнерной оркестрации |
| broker | Enrollment, ownership, grants/leases, OAuth, ledger mutations |
| httpapi | API/browser/proxy transport, hardening and bounded I/O |
| client | Signed Go integration SDK |
| app | Configuration и composition root |
| cmd | broker daemon и безопасный control CLI |
| internal | AEAD, filesystem, journal, outbound network и строгий JSON |
| tests/e2e | Процессы/роли на уровне HTTP-сервиса |

Domain не импортирует httpapi или app. Provider не знает о Hermes identity. Materialize не вызывает
Docker, shell или arbitrary exec. Broker не владеет tool projection. Architecture gate проверяет
список допустимых imports в core-слоях. Публичным extension boundary является provider SPI/HTTP SDK;
внутренние packages не являются стабильной библиотекой для внешних импортов.
