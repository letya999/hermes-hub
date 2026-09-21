---
description: "Границы ownership и четыре независимых контракта."
last_verified: "2026-09-18"
---

# Общая архитектура

```text
Communication Hub -- verified identity / approve --> Credential Broker
       |                                                  |
     Hermes --> ToolHub -- credential request / grant --> |
                    |                                     |--> SecretProvider
                    |                                     |    local / Vault / K8s
                    |                                     |--> encrypted metadata ledger
                    v                                     |
              Runtime adapter <-- lease/materialization --+
                    |                         |
                 ToolHive                 auth proxy
                    |                         |
               MCP workload -------------- Provider API
```

Broker не узнаёт владельца из полей браузера и не выбирает пользователя по Telegram ID.
ToolHub не получает plaintext через control API. Runtime adapter — отдельная доверенная роль,
которая вправе получить ENV/file materialization, но не создавать произвольное enrollment от чужого имени.

Четыре независимых контракта: что требуется (`contract`), кто вправе использовать (`grant`), где
находится (`provider.Ref`), как доставить (`Delivery/State/Route`). Общая связь — credential ID и revision,
а не hardcoded `user_id/secrets.env`.

В standalone приложении core собирает `app.Build`. При встраивании предпочтительно сохранить этот
composition root или использовать SDK через HTTP. Ledger backend и local provider шифруются отдельными
purpose-derived ключами. Источник master key — protected file; это конфигурация запуска, не user secret.
