---
description: "Как пройти настоящий HTTP enrollment без секретов в командной строке."
last_verified: "2026-09-18"
---

# Локальный запуск и проверка

## Подготовка

Исполняемая платформа MVP — Linux. На Windows использовать WSL2 или Linux VPS.
Python нужен только для команд качества и документации. Работающий сервис его не использует.
Установить `just` и актуальный Go, указанные в `tools.json`, затем:

```bash
just check
just build
just init /tmp/credential-broker-demo
just serve /tmp/credential-broker-demo/config.json
```

Последняя команда работает в текущем терминале. Во втором терминале выполняются следующие шаги.
`init` не перезаписывает существующие ключи. Для нового стенда выбрать новый пустой абсолютный каталог.
Для автономного запуска после сборки `just` не требуется: используются два бинарника из `.build/bin`.

## 1. ToolHub создаёт запрос

```bash
just ctl --key-file /tmp/credential-broker-demo/keys/toolhub.private \
  --actor-file /tmp/credential-broker-demo/actor.json \
  --method POST --path /v1/requests \
  --body-file "$PWD/examples/requests/create.json"
```

Ответ содержит `id` и `authorization_url`, но не секреты. Открыть URL в браузере на этом же компьютере.
На странице появится код подтверждения. **Одна ссылка не доказывает принадлежность пользователя.**

## 2. Communication Hub подтверждает браузер

В отдельном JSON-файле записать только `{"code":"код-со-страницы"}`. Это не токен внешнего провайдера.
В команде ниже заменить `REQUEST_ID` на `id` запроса. На настоящем Hub эту операцию выполняет адаптер
после получения кода из уже аутентифицированной личной беседы, а не модель по собственной инициативе.

```bash
just ctl --key-file /tmp/credential-broker-demo/keys/communication.private \
  --key-id communication --issuer hermes-communication --audience broker:approve \
  --actor-file /tmp/credential-broker-demo/actor.json \
  --method POST --path /v1/requests/REQUEST_ID/approve \
  --body-file /tmp/credential-broker-demo/approval.json
```

Обновить страницу. Появится форма GitHub token. Ввести токен **только в браузерную форму**.
Для локального теста без GitHub можно ввести вымышленное значение: этот контракт проверяет формат,
а реальный provider acceptance call остаётся обязанностью ToolHub. Не считать такой тест успешной
авторизацией на GitHub.

## 3. ToolHub получает результат и выдаёт grant

```bash
just ctl --key-file /tmp/credential-broker-demo/keys/toolhub.private \
  --actor-file /tmp/credential-broker-demo/actor.json \
  --path /v1/requests/REQUEST_ID
```

В ответе появится `credential_id`. Использовать его вместо `CREDENTIAL_ID`:

```bash
just ctl --key-file /tmp/credential-broker-demo/keys/toolhub.private \
  --actor-file /tmp/credential-broker-demo/actor.json \
  --method POST --path /v1/credentials/CREDENTIAL_ID/grants \
  --body-file "$PWD/examples/requests/grant.json"
```

Полученный `grant_id` передать в JSON `{"grant_id":"значение","ttl_seconds":60}` и вызвать
`POST /v1/leases` тем же control-клиентом. Либо использовать Go SDK: `Client.Grant`, `Client.Acquire`.

## 4. Runtime adapter получает секрет отдельно от ToolHub

Доверенный runtime adapter использует отдельный ключ `runtime.private`, audience `broker:runtime`
и Actor с точными `binding_id`/`workload_id`. Он вызывает `Client.Materialize`, монтирует возвращённые
sources в заданные targets через ToolHive и передаёт ENV процессу MCP.

`brokerctl` специально запрещает runtime API: plaintext ENV не должен попадать в терминал, agent
tool result или журнал команды. Полностью автоматический сценарий SDK и настоящего HTTP-сервера
проверяется `just test-e2e`.

## 5. Отозвать

Control-клиент выполняет `POST /v1/credentials/CREDENTIAL_ID/revoke` с пустым телом.
Broker запрещает существующие leases. ToolHub отдельно убирает tools и останавливает workload:
уже скопированный ENV нельзя стереть из чужого процесса.

## Другой MCP

Скопировать **после review** нужный JSON из `examples/contracts` в каталог контрактов стенда,
сохранить права владельца и перезапустить Broker. Каталог не импортирует произвольные GitHub README.
Для `google-oauth-proxy` дополнительно нужен admin-config OAuth; простое копирование этого контракта
без provider приведёт к намеренному отказу запуска.

[Эксплуатация](docs/operations/configuration.md) описывает production TLS, права и adapters.
[Интеграция](docs/architecture/hermes-integration.md) отделяет передачу refs от plaintext runtime delivery.
