---
description: "Native TLS, private directories, providers и согласование runtime paths."
last_verified: "2026-10-23"
---

# Конфигурация и развёртывание

## Production

Шаблон: [production.json](../../../../services/credential-broker/examples/config/production.json). Это конфигурационный пример,
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

## Маркер инициализации

`state_witness_dir` — optional absolute private directory для маркера ранее
инициализированного состояния; путь проверяется при `Build` через securefs.
Без него маркер хранится рядом с master key. Флаг `serve --state-witness-dir`
переопределяет поле конфигурации. Каталог должен быть доступен Broker для создания
файла при первой успешной инициализации сервиса. Compose infra передаёт флаг и
монтирует отдельный external volume `hermes-credential-broker-recovery-<env>` в
`/var/lib/credential-broker-recovery` только в Broker. `hubctl up` создаёт volume
перед запуском infra/Broker; обновлённый образ содержит каталог UID/GID10001,
mode0700, откуда Docker инициализирует пустой volume. Существующие непустые
volumes автоматически не chown/chmod; неверные права требуют проверки оператора.
При прямом Compose volume нужно создать заранее. Dev/prod имеют
разные volumes; запуск старого образа без нового флага не поддерживается.
Это только обнаружение пустого состояния, не encrypted backup или anti-rollback.

## Opt-in encrypted recovery

`recovery_dir`, `recovery_anchor_dir`, `recovery_key_file` и
`recovery_identity` включаются только вместе. `recovery_identity` — стабильный
уникальный идентификатор deployment и environment, например
`site-1/dev`; менять его без миграции нельзя. `recovery_key_file` содержит
отдельные 32 raw random bytes и не может совпадать с `master.key`. Каталоги
primary journal (`data_dir`), encrypted recovery и commit anchor не должны
вкладываться друг в друга; оператор размещает их на независимых носителях.
Ключ recovery должен сохраняться отдельно от primary state и recovery
ciphertext. Проверка путей не доказывает независимость Docker volumes.

Для новых пустых установок Broker создаёт recovery journal и зашифрованные
копии новых local provider objects до подтверждения записи. Каждая
подтверждённая запись фиксируется отдельным зашифрованным commit anchor.
Старый непустой journal нельзя просто открыть с новыми полями: остановить
Broker и выполнить `credential-broker migrate-recovery --config <absolute-path>`.
Команда переносит подтверждённые записи и только ссылки активных managed
credentials, незавершённых drafts и текущих checkpoints; её можно повторить
после прерывания. Она не печатает secret values. Порядок и пределы описаны
в [recovery.md](recovery.md).

Текущий Compose монтирует только independent state witness volume; он не
включает `recovery_dir` автоматически. Обновление dev-бинарника само по себе
не означает, что существующее состояние уже защищено резервной копией.

## Browser session approval

По умолчанию `/connect/<id>` сначала показывает одноразовый код привязки: форма открывается только
после `POST /v1/requests/<id>/approve` из доверенного канала Hub. Поле `direct_form` в config.json
или переменная `BROKER_DIRECT_FORM=1` при `serve` отключают этот шаг — форма доступна сразу по
ссылке. Режим предназначен для personal loopback-развёртываний: единственными барьерами остаются
секретность 128-битного id ссылки, TTL запроса и одноразовая сессия. На shared listener или при
пересылаемых ссылках режим не включать; TLS, Origin/CSRF и аудит он не затрагивает.

## Providers

[provider-snippets](../../../../services/credential-broker/examples/config/provider-snippets.json) содержит фрагменты, а не цельный Config.
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

Пример systemd: [unit](../../../../services/credential-broker/deploy/credential-broker.service). Dockerfile является build/runtime примером,
не обещает готовый volume orchestration. Init/provisioning каталогов выполняется отдельно от Broker.
Не использовать `--privileged`, Docker socket или shared host workspace для MCP.
