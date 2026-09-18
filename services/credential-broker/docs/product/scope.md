---
description: "Матрица реализованного поведения и явных неподдержанных сценариев."
last_verified: "2026-09-18"
---

# Границы MVP

| Область | В этой версии | Не входит |
| --- | --- | --- |
| Fields | Secret/string/json/pem/blob, fixed bounds, file upload | Произвольный исполняемый schema/UI plugin |
| Contracts | Admin-reviewed JSON, immutable revision/digest | Автоугадывание README, GitHub build/install |
| Enrollment | Link + approved browser + CSRF, TTL, idempotent creation | Новая user DB, Telegram bot или аккаунт-провайдер |
| Providers | Optional local, Vault KV v2 read/write, Kubernetes read-only | AWS/Azure/1Password adapters и Kubernetes write |
| OAuth | Static client, code+PKCE, state, refresh and rotation | Device flow, DCR, discovery, OIDC login, loopback relay |
| Files | ENV, exact read-only file, JSON mapping, несколько файлов | argv delivery, общий каталог между пользователями |
| State | Ограниченные плоские файлы, checkpoint/restore, один writer | Полный browser profile, coherent SQLite snapshot |
| Proxy | Fixed upstream, methods/paths, header/basic/bearer/OAuth/mTLS | CONNECT, transparent TLS MITM, произвольный URL, SSE/WebSocket |
| Runtime | Descriptors и read-only/tmpfs materialization | Docker/ToolHive runner, mount/chown/stop за ToolHub |
| Ledger | Encrypted append journal, fsync, cursors, one process | HA, распределённые транзакции, compaction, rollback-proof anchor |
| Revoke | Local fence, cancellation, leases/files invalidation | Гарантированный отзыв уже скопированного provider token |

## Граница совместимости

`google-file-session` демонстрирует доставку client JSON и state. Он не реализует OAuth внутри любого
Google Calendar MCP. Если конкретный сервер сам слушает localhost callback, нужен отдельный
проверенный adapter либо broker-owned OAuth с поддерживаемым MCP token-consumption contract.

`github-app-files` демонстрирует PEM/ID delivery. Он не утверждает, что официальный GitHub MCP
понимает указанные примерные env names или самостоятельно обменяет GitHub App key на installation token.

Фикстуры и schema проверяются локально. Живые vendor/MCP проверки выполняются в окружении интеграции.
