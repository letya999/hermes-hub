---
description: "Назначение сервиса, границы MVP и точки входа."
last_verified: "2026-09-18"
---

# Credential Broker

Самостоятельный Go-сервис между ToolHub, подтверждённой identity и учётными данными MCP.
Строит защищённые формы по проверенному контракту, проводит OAuth, выдаёт ограниченные leases,
материализует ENV/файлы/изменяемое состояние и проксирует разрешённые HTTP-запросы.

**Это не Vault и не его замена.** Локальный зашифрованный provider необязателен.
Реализованы адаптеры Vault KV v2 и Kubernetes Secrets; можно добавить свой через Go-интерфейс.
Ни Nango, ни OpenBao, ни Kubernetes не требуются для запуска. Сторонних runtime Go-модулей нет.

## Что внутри

| Контур | Реализовано |
| --- | --- |
| Ввод | Secret/string, несколько полей, JSON, PEM, binary upload, привязка браузера через Communication Hub |
| OAuth | Предзарегистрированный клиент, Authorization Code + PKCE S256, проверка state, сериализованный refresh |
| Хранение | Ссылки на заменяемые providers; optional local AEAD, Vault KV v2 read/write, Kubernetes read-only |
| Доставка | ENV, точные пути read-only файлов, JSON из полей, ограниченный writable state, HTTP auth и mTLS |
| Владение | User/context owner, явные grants нескольким bindings, shared-политика и отдельные leases |
| Жизненный цикл | Одноразовое enrollment, rotation, revoke, delete, зашифрованный durable ledger, cursor событий |
| Интеграция | Версионированный HTTP API, Go DTO/SDK, отдельные identity audiences, два небольших CLI |

## Запуск

Требуются Linux, Go, Python 3.10+ и `just`. Для production использовать версию Go из `tools.json`,
а не минимальную языковую версию `go.mod`.

```bash
just check
just build
just init /tmp/credential-broker-demo
just serve /tmp/credential-broker-demo/config.json
```

`init` создаёт **только локальный dev-стенд**. HTTP слушает loopback; ключи генерируются при запуске,
а не поставляются в архиве. Не публиковать dev-порт наружу.

## Открыть дальше

| Документ | Для чего |
| --- | --- |
| [QUICKSTART](QUICKSTART.md) | Полный сценарий с браузером, подтверждением и CLI |
| [Интеграция Hermes Hub](docs/architecture/hermes-integration.md) | Контракты, SDK, runtime adapter и перенос в монорепозиторий |
| [API](docs/architecture/api-contract.md) | Identity assertion, коды ошибок и OpenAPI |
| [Границы MVP](docs/product/scope.md) | Что работает и что намеренно не реализовано |
| [Безопасность](SECURITY.md) | Условия доверия и ограничения гарантий |
| [Проверки](.work/done/CHG-001-credential-broker/artifacts/verification.md) | Фактически выполненные команды, покрытие и незапущенные проверки |
| [Документация](docs/index.md) | Архитектура, эксплуатация, решения и правила |

Порог покрытия — **85% statement coverage суммарно и в каждом production-пакете**, без исключения
неудобных веток. Это проверяемый gate, не утверждение о безопасности по одному проценту.
Архив содержит исходники, а не production-бинарник. Независимый аудит и живое подключение к
Hermes/ToolHive/Google в этой поставке не выполнялись; границы проверки указаны в отчёте.
