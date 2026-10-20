---
description: "Динамическая регистрация collect-and-deliver контрактов через control API."
last_verified: "2026-10-16"
---

# ADR-0067 (imported from services/credential-broker ADR-0008): динамическая регистрация контрактов через control API

Status: accepted (imported from services/credential-broker).

## Контекст

Каталог контрактов читался из `contracts_dir` один раз при старте. Подключение любого нового
коннектора требовало ручного JSON от оператора и рестарта брокера — процесс не масштабируется
на «установить произвольный reviewed-коннектор», а сам файл дублирует метаданные, которые ToolHub
уже знает из reviewed definition (имена полей, обязательность, env-таргеты).

## Варианты

- Оставить file-only каталог и генерировать файлы — рестарт брокера на каждый коннектор и
  ручной шаг оператора сохраняются.
- Открыть регистрацию контрактов пользователям — contract перестаёт быть reviewed boundary,
  произвольный signer смог бы объявить сбор чужих env-имён.
- `POST /v1/contracts` под audience `broker:control`, журналируемую и воспроизводимую через
  replay — выбрано. Согласие человека остаётся на защищённой форме `/connect/{request}`:
  контракт описывает только shape, значения вводит владелец.

## Решение

`Broker.RegisterContract(actor, contract)` принимает контракт от аутентифицированного
control-plane actor (ToolHub). Ограничения динамических контрактов: только collect-and-deliver —
без routes, OAuth provider, state и `allow_shared`; storage по умолчанию — детерминистически
первый writable provider. `id@revision` иммутабелен: повторная регистрация с тем же digest —
идемпотентна, с другим — conflict. Контракт персистится mutation `contract` в journal и
восстанавливается replay до drift-check; файл с тем же ключом и другим digest делает старт
fail-closed. Успешная регистрация пишет audit-событие `contract.registered` с `id@revision`.

## Последствия

Затраты: новый mutation record, endpoint, клиентский метод, тесты на валидацию/идемпотентность/
конфликт/рестарт/audience. Ограничения: динамические контракты не заменяют reviewed-файлы для
routes/OAuth/state/shared — эти режимы остаются операторскими. ToolHub синтезирует контракт из
reviewed definition; contract с секретами не работает — значения по-прежнему вводятся через форму.

## Когда пересматривать

Если потребуется динамическая регистрация routes/OAuth/shared или миграция file-каталога
в journal полностью.
