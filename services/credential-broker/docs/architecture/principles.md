---
description: "Правила, которые нельзя размывать ради удобного подключения нового MCP."
last_verified: "2026-09-18"
---

# Архитектурные правила

Без подтверждённого principal, reviewed contract или storage availability доступ запрещается.
Назначение provider URL и mount target — полномочие администратора контракта, не модели.

Secret value, credential ID, external connection и execution scope — разные сущности. Opaque ID не
заменяет authorization check. Shared runtime не означает право читать чужие credentials.

Модель не получает signing key, runtime audience, Docker socket или произвольный provider locator.
HTTP request связывается подписью с точным телом. Нельзя переслать действующий control assertion
вместо upstream Authorization или runtime assertion.

Каждая новая абстракция подтверждается реальным вертикальным тестом. Новые языковые сборщики,
контейнерная оркестрация и secret-store UI не относятся к этому компоненту.
