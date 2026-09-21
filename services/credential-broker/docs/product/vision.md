---
description: "Продуктовая роль: согласовать credentials MCP с identity и выбранным хранилищем."
last_verified: "2026-09-18"
---

# Зачем нужен Broker

ToolHub говорит: этому проверенному MCP нужны такие поля и такой способ доставки. Broker возвращает
защищённую процедуру подключения, затем opaque credential reference и ограниченный lease.

Пользователь не копирует secret в Telegram, Hermes prompt или runtime YAML. Команды не поддерживают
отдельные ручные решения для каждого `credentials.json`. Интегратор добавляет reviewed contract,
а provider хранения выбирается независимо от MCP. Каноническая identity берётся из Communication Hub.

Успех Broker — корректно завершённое enrollment. Успех ToolHub — MCP handshake, безопасный provider
acceptance call и tool projection. Смешивать эти два состояния нельзя.
