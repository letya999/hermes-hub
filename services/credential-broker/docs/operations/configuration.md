---
description: "Native TLS, private directories, providers и согласование runtime paths."
last_verified: "2026-09-18"
---

# Конфигурация и развёртывание

## Production

Шаблон: [production.json](../../examples/config/production.json). Это конфигурационный пример,
не готовый секретный installation. Адрес example.com, TLS certificates и реальные permissions
оператор задаёт явно. `check-config` проверяет синтаксис и базовые поля; полный adapter/catalog/TLS
preflight выполняет `app.Build` при serve.

Запуск non-root. Каталоги data/contracts/keys/providers/runtime принадлежат service UID, mode0700;
ключи и tokens — обычные single-link files0600. Parent paths не должны быть symlink. Broker runtime
root обязан лежать на настоящем tmpfs; простое имя `/run` само по себе этого не доказывает.

Native TLS обязателен при `dev_http=false`; production не доверяет X-Forwarded-* для обхода TLS.
Можно использовать прямой порт8443 либо TCP/TLS passthrough перед ним. Если нужен terminating ingress,
переделать boundary отдельным review/ADR, а не включать dev_http на публичном listener.
Public origin должен совпадать с фактическим Host, включая порт. OAuth redirect зарегистрирован точно.

`master.key` — 32 raw random bytes для шифрования metadata и optional local provider, не пользовательский
PAT. Private identity keys остаются в соответствующих Hub/runtime services; Broker получает только
их raw32 public keys. Dev init создаёт все роли рядом только для лаборатории.

## Providers

[provider-snippets](../../examples/config/provider-snippets.json) содержит фрагменты, а не цельный Config.
Vault фиксирует mount/prefix и optional namespace. Kubernetes фиксирует namespace и read-only роль.
Не выдавать wildcard list/write RBAC. Private API endpoint требует exact allowed CIDR и trusted CA;
metadata/link-local не разрешаются даже CIDR exception.

Service account projected tokens Kubernetes обычно используют symlink layout. Строгий file reader
этой версии их отвергает. Передать regular private token file через доверенный materializer либо
добавить отдельный scoped credential-agent adapter. Не ослаблять NOFOLLOW глобально.

Standalone proxy routes используют публичные HTTPS destinations. Private/custom-CA proxy возможно
через in-process `httpapi.Config.NetworkPolicy`; отдельного global bypass config у daemon нет.
OAuth token endpoints в standalone также публичные HTTPS, без автоматического discovery.

## Runtime и UID

Materializer не выполняет chmod/chown произвольного target и не монтирует контейнеры. ToolHive adapter
обеспечивает same numeric UID либо корректное user-namespace/idmapped mapping для чтения0400 файлов.
Broker и adapter видят один source path. Writable state directory монтируется только нужному workload.
Mount source никогда не приходит из browser upload filename.

Пример systemd: [unit](../../deploy/credential-broker.service). Dockerfile является build/runtime примером,
не обещает готовый volume orchestration. Init/provisioning каталогов выполняется отдельно от Broker.
Не использовать `--privileged`, Docker socket или shared host workspace для MCP.
