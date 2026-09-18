---
description: "Негативные сценарии, контрмеры и остаточный риск."
last_verified: "2026-09-18"
---

# Модель угроз

| Угроза | Барьер | Что остаётся |
| --- | --- | --- |
| Утекла form URL | Отдельный pairing approval, owner check, TTL | Пользователь может подтвердить код атакующего по социальной инженерии |
| LLM выбрала чужой owner | Signed canonical Actor, серверная проверка ownership | Компрометация доверенного Hub signer |
| Перенос control token в runtime | Audiences и отдельные ключи | Не выдавать всем ролям один private key |
| Подмена request body | Ed25519 method/URI/body hash | Replay допустимого HTTP assertion до expiry; бизнес-idempotency отдельно |
| Чужой grant/lease ID | Полный identity tuple + policy + revision | Trusted runtime обязан правдиво указывать workload identity |
| Shared MCP смешивает token | Process contracts одного credential, proxy-only exception | Общая память самого MCP может смешать данные; нужна runtime оценка |
| SSRF/metadata/DNS rebinding | Fixed host, dial-time IP validation, deny redirects | Разрешённый провайдер может иметь опасные собственные API |
| Secret попал в output | Positive header allowlist, echo guard, runtime-only DTO | Echo guard не DLP; transformed secret/data exfiltration не универсально блокируются |
| Traversal/symlink/hardlink | Canonical targets, openat NOFOLLOW, owner/nlink checks | Скомпрометированный root/тот же trusted UID |
| Повреждение ledger | AEAD, цепочка, fail-closed startup | Целый корректный старый префикс без внешнего anchor |
| Revoke во время запроса | Epoch fence + context cancellation | Уже выполненный провайдером эффект |
| Утечка tmpfs | Restricted UID/mounts, teardown | Swap/dumps, копии в памяти чужого процесса |
| Resource exhaustion | Bounds, concurrency, bounded ledger/session/lease counts | DoS доверенным signer остаётся возможен |

Нет сетевого «sandbox обещания»: сетевые namespaces и egress MCP задаёт ToolHive/оператор.
Broker не защищает от контейнерного escape и не владеет source supply-chain MCP.
