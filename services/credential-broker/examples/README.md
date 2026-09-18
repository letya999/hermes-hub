---
description: "Как использовать reviewed JSON примеры без ложного обещания совместимости."
last_verified: "2026-09-18"
---

# Примеры контрактов и запросов

`contracts` содержит валидные схемы: GitHub PAT, Notion token, Google client JSON+state, service account,
GitHub App PEM, restricted HTTP proxy, native OAuth proxy, mTLS files и JSON mapping.
Проверка TestShippedContractsAreValid парсит все примеры тем же Go validator.

Это образцы credential patterns. Exact variable names и пути нужно подтвердить для выбранной версии
MCP. `github-app-files` не означает поддержку App-auth официальным GitHub MCP. `google-file-session`
не запускает OAuth listener MCP и не преобразует broker tokens в vendor runtime_session.json.

`requests` содержит только non-secret payloads для dev actor из init. Pairing code заменяется на
реальное значение страницы, не на OAuth token. `config/provider-snippets.json` — отдельные фрагменты
для включения в Config, не готовый файл serve.
