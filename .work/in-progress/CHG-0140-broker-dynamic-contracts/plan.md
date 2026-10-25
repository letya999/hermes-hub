---
description: "Динамическая регистрация credential contracts через control API."
last_verified: "2026-10-16"
---

# Динамическая регистрация контрактов

## Проблема

Каталог контрактов читается из `contracts_dir` один раз при старте. Установка произвольного
коннектора требует ручного JSON от оператора и рестарта — это не масштабируется и было отклонено
владельцем как hardcode-процесс.

## Решение

`POST /v1/contracts` (audience `broker:control`) регистрирует reviewed-by-form контракт в ledger:
- ограничения динамических контрактов: только env/file/json_file deliveries, без routes, без
  OAuth, без state, `allow_shared=false`; storage по умолчанию — первый writable provider;
- идемпотентность: тот же digest по `id@revision` возвращает существующий, другой digest —
  conflict; revisions immutable;
- персистентность: mutation `contract` в journal, replay восстанавливает в catalog до drift-check;
- аудит: `contract.registered` с actor и `id@rev`; секретов в контракте нет по построению.

Согласие пользователя — существующая защищённая форма `/connect/{request}` (поля и targets
видны до submit). ToolHub синтезирует контракт из reviewed definition; произвольный signer
не создаёт контрактов — audience `broker:control` есть только у ToolHub.

## Шаги

1. `broker`: contractRecord + RegisterContract + replay.
2. `httpapi`: route POST /v1/contracts; `client`: RegisterContract.
3. Негативные/позитивные тесты: валидация, идемпотентность, конфликт, рестарт, аудит, audience.
4. ADR-0008, scope.md, api-contract.md, spec.
5. `just check` брокера; интеграция в ToolHub — в корневом CHG-0107.
