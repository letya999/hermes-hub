---
description: "Перед использованием с настоящими секретами: границы защиты и эксплуатационные обязанности."
last_verified: "2026-09-18"
---

# Модель безопасности и раскрытие уязвимостей

## Что считается доверенным

Администратор узла, процесс Broker, master key, утверждённые contracts, внешние secret providers,
Communication Hub identity resolver и runtime adapter. Модель и MCP не получают signing keys.
Root узла, скомпрометированный runtime adapter или signer могут обойти эти границы: одна машина
не является аппаратной изоляцией от администратора.

Сервис не проходил независимый аудит. Защита реализована и проверяется отрицательными тестами,
но фраза «очень безопасно» не является сертификатом или доказательством отсутствия дефектов.

## Реализованные барьеры

Три purpose-separated Ed25519 audiences; подпись точного method/URI/body; короткий срок assertion;
подтверждение конкретного браузера через Communication Hub; single-use submit, CSRF и exact Origin;
проверка grant/principal/context/runtime/binding/workload/policy/revision при каждом runtime вызове.

TLS без отключения проверки сертификата, ограничение SSRF и DNS-rebinding в исходящем dialer,
запрет редиректов API, запрет forwarding входящих токенов/cookies, ограниченные тела и конкуренция.
Файлы без symlink/hardlink, конкретные targets, production tmpfs, fsync ledger до успешного ответа.
На ошибки хранения — отказ, а не разрешение доступа.

## Чего эти барьеры не обещают

* ENV и file delivery раскрывают значение самому MCP. Только proxy-mode скрывает upstream credential;
  он всё равно даёт разрешённое действие от имени пользователя. Allowlist домена не запрещает
  выгрузить данные в разрешённый ресурс того же провайдера.
* Revoke запрещает новые broker operations и отменяет отслеживаемые HTTP-запросы. Уже выполненный
  внешний эффект, скопированный токен или чужой запущенный процесс он не отменяет.
* tmpfs может попасть в swap или crash dump. Оператор отключает swap/dumps либо шифрует их;
  Go GC и immutable strings не позволяют доказать полное обнуление каждой копии памяти.
* AEAD и hash-chain выявляют изменения/неполный хвост ledger, но не подмену всего ledger старым
  корректным префиксом. Нужны защищённые backups и внешний checkpoint для защиты от rollback.
* Browser/CLI auth-state ограничен плоским списком файлов. Snapshot требует остановленного writer;
  `quiesced=true` — утверждение доверенного runtime, а не автоматическая проверка остановки.
* Нет автоматического upstream OAuth revocation, provider-wide ключевой ротации, кластера HA,
  device flow, DCR, произвольного browser profile или универсального localhost OAuth relay.

## Перед production

Пройти `just release-check` на поддерживаемом toolchain, отдельно просмотреть threat model,
выполнить живые contract-тесты с вашим Vault/Kubernetes/Google и readiness MCP. Ограничить egress
контейнеров на уровне ToolHive/сети, изолировать namespaces/mounts, не выдавать MCP Docker socket.
Не публиковать `.build`, master key, provider tokens, дампы памяти или приватные Hub signing keys.

[Отчёт поставки](.work/done/CHG-001-credential-broker/artifacts/verification.md) перечисляет, какие
проверки действительно запускались. Отсутствие сторонних Go-модулей не исключает уязвимости stdlib.

## Сообщить о проблеме

В upstream-проекте использовать приватный security advisory, если он включён владельцем;
иначе связаться с владельцем Hermes Hub по его подтверждённому каналу. Не отправлять живые токены,
ledger или конфиги с private paths в публичный issue. Отдельная security-почта здесь не выдумана.
