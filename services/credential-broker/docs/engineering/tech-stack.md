---
description: "Runtime без сторонних Go-модулей и отдельный набор инструментов качества."
last_verified: "2026-09-18"
---

# Стек и зависимости

Runtime: Go standard library, Linux filesystem primitives, native net/http TLS, Ed25519, AES-GCM,
append-only file journal. Нет обязательных БД, Redis, Kubernetes, Nango, Vault или sidecar-платформы.
Vault/Kubernetes adapters используют ограниченный HTTPS REST transport.

`go.mod` задаёт language floor 1.23, чтобы SDK и core можно было проверить доступным compiler.
Production toolchain зафиксирован отдельно в `tools.json` и должен совпасть с Hermes.
Нельзя использовать устаревший compiler в production только потому, что код компилируется на нём.

Development: Python 3.10+ stdlib и just. Release quality: pinned staticcheck/gosec/govulncheck,
устанавливаются только явной командой `just tools-install` в `.tools/bin`; runtime их не использует.
Версии инструментов обновляются осмысленно с прогоном тестов, не через непредсказуемый @latest.
