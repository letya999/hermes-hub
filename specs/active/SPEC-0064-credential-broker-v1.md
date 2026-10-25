---
description: "Замороженный контракт MVP и трассировка требований на исполняемые тесты."
last_verified: "2026-09-18"
---

# Credential Broker v1

## Задача

Отдельный Go Credential Broker для интеграции с ToolHub и Communication Hub без замены secret stores.

## Требования

### R1 — Контракт отдельно от хранения

Принимать только reviewed contract ID/revision; проверять fields/delivery/routes; неизвестное и drift отвергать.

### R2 — Подтверждённая identity

Связывать каждый control/runtime вызов с canonical Actor, точным методом/URI/body и audience.

### R3 — Защищённая форма

URL не открывает secret form без approve конкретного браузера; enforce TTL, CSRF, Origin, single submit.

### R4 — Idempotent enrollment

Повторный request той же identity и payload не дублирует операцию; другая identity не использует запрос.

### R5 — Providers независимы

Поддерживать optional local, Vault KV2 и read-only Kubernetes; external alias не принимает произвольный locator от браузера.

### R6 — Несколько bindings и shared

Явно проверять ownership и grant tuple; не смешивать разные process secrets; запретить shared mutable state.

### R7 — Краткоживущие leases

Проверять credential revision, policy, identity, expiry; revoke не обходится старой сессией.

### R8 — Безопасная materialization

Точные file targets, private modes, env path, bounded JSON, symlink/hardlink denial, production tmpfs.

### R9 — Runtime state

Раздельный state по binding, один writer, quiesced checkpoint и restore после restart.

### R10 — OAuth subset

Code+PKCE, bound state, refresh serialization/rotation; провал refresh закрывает доступ и требует нового flow.

### R11 — Credential proxy

Fixed destination, allowlisted method/path, no redirect/credential forwarding, mTLS и in-flight revoke.

### R12 — Durable ledger

Fsync before success, fail-closed storage errors, restart сохраняет enrollment и не сохраняет leases.

### R13 — Секреты не проходят через control

Plaintext только runtime API, auth-bearing data не логируются; ошибки sanitised, cross-role доступ запрещён.

### R14 — End-to-end boundary

Проверить app/HTTP/browser/SDK/runtime и exact file+state через restart без live-secret fixtures.

### R15 — Исполняемое качество

85% total и каждого production package, race, static checks, fuzz, актуальная документация; release-scanners отдельный fail-closed gate.

## Чего не делаем

Не собираем MCP/OCI, не запускаем ToolHive, не создаём identity provider, canonical Vault или универсальный
OAuth connector. Нет HA, transparent CONNECT, произвольных browser profiles и гарантии стирания чужой памяти.

## Критерии приёмки

| Требование | Проверка | Граница evidence |
| --- | --- | --- |
| R1 | `TestContractAdmission` | Положительные и отрицательные тестовые сценарии |
| R2 | `TestAssertions` | Положительные и отрицательные тестовые сценарии |
| R3 | `TestBrowserCSRFMultipartAndExpiredLinks` | Положительные и отрицательные тестовые сценарии |
| R4 | `TestEnrollmentIdentityReplayAndIdempotency` | Положительные и отрицательные тестовые сценарии |
| R5 | `TestExternalAliasAndBackendFailures` | Локальные executable tests; не live vendor E2E |
| R6 | `TestSharedOwnershipAndSeveralBindings` | Положительные и отрицательные тестовые сценарии |
| R7 | `TestLeaseMaterializationRevokeAndRotation` | Положительные и отрицательные тестовые сценарии |
| R8 | `TestFilesStateAndCleanup` | Положительные и отрицательные тестовые сценарии |
| R9 | `TestStateCheckpointsExclusiveLeaseAndRestart` | Положительные и отрицательные тестовые сценарии |
| R10 | `TestOAuthPKCERefreshAndConcurrentUse` | Локальные executable tests; не live vendor E2E |
| R11 | `TestProxyFixedDestinationHeaderIsolationAndRevocation` | Локальные executable tests; не live vendor E2E |
| R12 | `TestFailClosedJournalAndStateFilesystem` | Положительные и отрицательные тестовые сценарии |
| R13 | `TestHTTPEnrollmentRuntimeAndAudit` | Положительные и отрицательные тестовые сценарии |
| R14 | `TestE2EExactFileAndDurableRuntimeSession` | Локальные executable tests; не live vendor E2E |
| R15 | `TestProductionLayerBoundaries` | just check и just release-check; отсутствующий scanner не считается пройденным |
