---
description: "Первичные материалы, даты чтения и явные отличия от upstream."
last_verified: "2026-09-18"
---

# Источники и адаптация шаблона

Источники просмотрены 2026-09-18. Ссылки — основание для решения, не доказательство запуска
чужого проекта или полного аудита всех его файлов.

| Источник | Что использовано |
| --- | --- |
| [memory_bank_setup](https://github.com/letya999/memory_bank_setup) | Правильное имя репозитория и четыре режима знания |
| [medium](https://github.com/letya999/memory_bank_setup/blob/dev/memory_bank/sizes/medium.yaml) | Группы product/architecture/engineering/operations/docs |
| [AGENTS](https://github.com/letya999/memory_bank_setup/blob/dev/AGENTS.md) | just, frontmatter, generated indexes, evidence |
| [spec template](https://github.com/letya999/memory_bank_setup/blob/dev/memory_bank/project_templates/specs/spec-template.md) | Задача/требования/не-цели/приёмка |
| [work template](https://github.com/letya999/memory_bank_setup/blob/dev/memory_bank/project_templates/.work/change-template/state.yaml) | Прогресс и review в отдельном state |
| [Hermes architecture](https://github.com/letya999/hermes-hub/blob/feat/scoped-homes-communication-hub/docs/architecture.md) | Communication Hub → Hermes → ToolHub; отдельные ownership/runtime axes |
| [Hermes ADR0011](https://github.com/letya999/hermes-hub/blob/feat/scoped-homes-communication-hub/docs/adr/ADR-0011-stable-identity-identifiers.md) | Canonical IDs, external identity отдельно от principal |
| [Hermes issue105](https://github.com/letya999/hermes-hub/issues/105) | File path и mutable auth-state вместо только ENV |
| [OAuth BCP](https://www.rfc-editor.org/rfc/rfc9700.html) | OAuth boundary и рекомендации безопасности |
| [PKCE](https://www.rfc-editor.org/rfc/rfc7636.html) | S256 challenge/verifier |
| [Vault KV v2](https://developer.hashicorp.com/vault/api-docs/secret/kv/kv-v2) | Wire adapter, versions, CAS |
| [Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/) | Secret data/resourceVersion и namespace scope |
| [Go downloads](https://go.dev/dl/) | Production toolchain, отдельно от доступного локального compiler |

## Явные адаптации

Код upstream runtime не форкался. Применены его conventions; checker для этого проекта написан отдельно,
без PyYAML/Node. State — JSON-совместимый YAML1.2. Site/Mermaid tooling не требуются. `.env` не используется
для secret values: protected files и providers важнее буквального копирования общего шаблона.

Верхний AGENTS upstream содержит старую форму метаданных, но его действующее правило требует ровно
`description/last_verified`; здесь применено это правило. Две CI-системы используют одинаковые just targets.
Upstream commit SHA не получен: дата и branch указаны честно, без вымышленного pin.
