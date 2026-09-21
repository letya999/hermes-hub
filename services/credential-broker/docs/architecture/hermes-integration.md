---
description: "Как присоединить модуль без переписывания Communication Hub, ToolHub и ToolHive."
last_verified: "2026-09-18"
---

# Интеграция с Hermes Hub

## Ответственность адаптеров

| Компонент | Добавить в Hermes Hub | Чего не переносить |
| --- | --- | --- |
| Communication Hub | Существующая verified identity → approve assertion, доставка URL и pairing confirmation | Собственную user DB в Broker |
| ToolHub | Reviewed contract ID/revision, create request, poll events, grants, acquire/revoke | Secret values в jobs/tool projection |
| Runtime adapter | Отдельный signer, exact workload identity, materialize/mount, renew, stop/checkpoint/release | Runtime signer в MCP container |
| ToolHive | Реальные read-only mounts, ENV, dedicated/shared isolation, egress policy | Новый container runner в Broker |

Сервис не изменяет существующий репозиторий Hermes Hub и не утверждает, что адаптеры уже в него влиты.
Это отдельный модуль с готовой HTTP-границей и SDK. Связь с реальным Communication Hub/ToolHive должна
пройти acceptance после добавления adapters.

## Вариант 1: отдельный процесс

Запустить `credential-broker serve --config /absolute/config.json`. ToolHub импортирует `client`,
`api/v1`, `identity`; адрес, ключ и роль берёт из собственного trusted config.
Для локальной разработки Go-монорепозитория добавить модуль в `go.work` или использовать локальный
`replace github.com/letya999/credential-broker => ../credential-broker`.

```go
// canonical заполняет серверный identity resolver, не model arguments.
ctl, err := client.New(origin, client.Signer{
    KeyID: "toolhub", Issuer: "hermes-toolhub", Audience: "broker:control",
    PrivateKey: toolHubPrivateKey,
}, canonical, nil)
if err != nil { return err }
request, err := ctl.CreateRequest(ctx, v1.CreateRequest{
    ContractID: "github-pat", ContractRevision: 1,
    ConnectionID: connectionID, OnboardingID: onboardingID,
    IdempotencyKey: onboardingID, OwnerKind: "user",
})
if err != nil { return err }
// Доставить request.AuthorizationURL через Communication Hub.
// Дальше ждать credential.ready в ledger; не ожидать внутри одного LLM-turn.
```

Go SDK не содержит OAuth secrets и не зависит от остального Hermes. `api/v1.Materialized` отделён
по роли API, но Go type сам по себе не запрещает логирование: runtime integration обязан соблюдать границу.

## Вариант 2: модуль внутри дерева монорепозитория

Перенести каталог, например в `services/credential-broker`, сохранив отдельный `go.mod`, и включить
через `go.work`. Это самый дешёвый путь без массовой замены import paths. Для одного общего `go.mod`
понадобится механически заменить префиксы imports и обновить architecture/coverage gate module prefix.

In-process запуск доступен через `app.Build(Config)` и `httpapi.Server`, но тогда компрометация общего
процесса получает доступ к памяти Broker. «Отдельный package» не является отдельной security boundary.
Пакеты `internal/*` намеренно не предоставлены внешнему модулю как публичный SPI.

## Durable onboarding

ToolHub сохраняет `onboarding_id → request_id` и свой events cursor. Повторный Create с тем же
idempotency key и тем же телом возвращает существующий request; изменённое тело даёт conflict.
Событие credential.ready означает только завершённый enrollment. Далее ToolHub запускает workload,
делает initialize/tools-list/read-only provider acceptance и обновляет projection.

## Files и auth-state

Materializer создаёт plaintext на broker-side tmpfs и возвращает host source + exact target.
Runtime должен видеть **тот же source path**: standalone на общем Linux host проще, чем broker в
изолированном контейнере. В контейнерном варианте явно согласовать volume/path mapping.
Сервис не вызывает mount/chown и не выдаёт себе root; владельца и UID mapping обеспечивает runtime.

Перед checkpoint runtime останавливает writer, затем отправляет `checkpoint=true, quiesced=true`.
После restart Broker старые leases не воскресают. Runtime должен убрать старую projection/процессы,
запросить свежий lease и восстановить state. Нельзя использовать восстановленный файл с прежним lease ID.

## Proxy

Это обратный API proxy, не универсальная настройка `HTTP_PROXY`. MCP должен разрешать смену API base URL,
а trusted runtime adapter обязан сопоставлять вызов с principal/binding/lease и подписывать каждый запрос.
Когда MCP этого не умеет — ENV/file delivery в отдельном процессе. Без такой маршрутизации нельзя
разделять один процесс между пользователями с разными credentials.

## Удаление и отзыв

На revoke сразу запретить вызовы через старую projection, остановить соответствующий MCP, отменить
renew и удалить runtime mount. Broker отзывает локальное разрешение, не делает автоматический
provider-global OAuth revocation. Не удалять общий credential при удалении одного из нескольких bindings.
