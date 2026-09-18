---
description: "Какие тесты доказывают поведение сервиса и где заканчивается локальная проверка."
last_verified: "2026-09-18"
---

# Пирамида тестирования

## Основание: unit и отрицательные границы

Табличные тесты проверяют claims/audiences, ID/schema/value validation, duplicate JSON, пути,
PEM/JSON, scopes, deadlines, provider bundle semantics. Используются собственные clocks и короткие
CSPRNG-generated тестовые credentials; настоящие tokens в fixtures не хранятся.

Filesystem tests намеренно проверяют symlink/hardlink/permissions, wrong key и corrupted journal.
Их удобно запускать в базовом suite, хотя они касаются реального локального filesystem.
Architecture tests анализируют Go imports/AST и не допускают прямой os/exec, unsafe или TLS bypass.

## Середина: component/integration

Broker + real encrypted journal + replaceable provider + materializer. HTTP tests используют
httptest TLS servers для OAuth/provider и проверяют signatures, CSRF, multipart limits, mTLS,
redirect refusal, cross-user, in-flight revoke, alias failures, single-writer state и restart.
Локальный fake отвечает по реальному wire contract; это не живой Vault/Kubernetes кластер.

## Верх: E2E

`tests/e2e` запускает app composition и настоящий TCP listener. Отдельные SDK clients имитируют
ToolHub, Communication Hub и trusted runtime; браузер имеет cookie jar. Первый сценарий проходит
request → browser approve → submit → grant → lease → materialize → revoke. Второй проверяет exact
JSON target и durable runtime_session checkpoint через restart. Реальных Hermes/ToolHive/Google
processes в тестовом окружении нет, поэтому эти тесты называются E2E **сервиса**, не всей платформы.

## Race, fuzz и воспроизводимость

Race detector включён во всей coverage-сборке. Fuzz targets: FuzzVerifier, FuzzPaths, FuzzDecode.
Короткий fuzz smoke — регрессионный safeguard, не длительная security campaign. Перед production
дополнительно запускать расширенные campaigns и live read-only acceptance в тестовом tenant.
