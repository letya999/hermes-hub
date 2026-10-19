---
description: "Динамическая регистрация collect-and-deliver контрактов через broker:control."
last_verified: "2026-10-16"
---

# Credential Broker: динамические контракты

## Задача

Дополнение к v1 (ADR-0008): reviewed ToolHub definition может регистрировать collect-and-deliver
контракт через control API без ручного файла и рестарта. Согласие пользователя остаётся на
защищённой форме; контракт несёт только shape, не секреты.

## Требования

### R1 — Аутентифицированная регистрация

`POST /v1/contracts` принимает только audience `broker:control` с валидным Actor; прочие
audience и подписи отклоняются.

### R2 — Только collect-and-deliver

Динамический контракт не содержит routes, OAuth provider, state и `allow_shared`; storage по
умолчанию — детерминистически первый writable provider, неизвестный storage отклоняется.

### R3 — Иммутабельный id@revision

Повторная регистрация с тем же digest идемпотентна; тот же `id@revision` с другим digest —
conflict; новая revision регистрируется отдельно.

### R4 — Журнальная персистентность

Успешная регистрация фиксируется mutation в journal до ответа; replay восстанавливает контракт
в каталог; файл с тем же ключом и другим digest делает старт fail-closed.

### R5 — Аудит и отсутствие секретов

Регистрация пишет `contract.registered` с `id@revision`; API не принимает и не возвращает
значения секретов — поля контракта описывают только shape.

## Чего не делаем

Не регистрируем динамически routes, OAuth, runtime state и shared-workload контракты — они
остаются в операторском file-каталоге. Не открываем регистрацию пользователям или runtime.
Не заменяем форму согласия: контракт без submit не даёт credential.

## Критерии приёмки

| Требование | Проверка | Граница evidence |
| --- | --- | --- |
| R1 | `TestHTTPDynamicContractRegistration` | Положительные и отрицательные тестовые сценарии |
| R2 | `TestDynamicContractRegistrationBounds` | Положительные и отрицательные тестовые сценарии |
| R3 | `TestDynamicContractRegistration` | Положительные и отрицательные тестовые сценарии |
| R4 | `TestDynamicContractSurvivesOnlyWhenCatalogMatches` | Положительные и отрицательные тестовые сценарии |
| R5 | `TestDynamicContractRegistration` | Аудит-событие проверяется тестом; секреты вне scope контракта |
