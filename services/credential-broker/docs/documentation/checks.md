---
description: "Что проверяет dependency-free checker, а что он не обещает."
last_verified: "2026-09-18"
---

# Реестр проверок документации

| Код | Условие |
| --- | --- |
| MD001–MD002 | Точный frontmatter, дата, H1 |
| MD003–MD005 | Один H1, whitespace/newline, лишние пустые строки |
| MD006 | Индекс совпадает с генератором |
| MD007 | Локальные ссылки ведут в существующие файлы внутри репозитория |
| SP001–SP003 | Sections spec, requirements-to-acceptance, тестовые symbols существуют |
| SP004 | Frozen spec соответствует baseline hash |
| WK001–WK003 | State schema, дата и существующее evidence |
| API001–API002 | Внутренние OpenAPI refs и уникальные operation IDs |
| CI001 | Run steps используют just recipes |

Это узкий Python stdlib checker под адаптированный template, не все правила markdownlint/Mermaid.
Он не проверяет доступность внешних URL и не заявляет, что Go-код из prose примеров компилировался.
Go SDK отдельно проверяется полноценными executable tests. `.markdownlint.json` можно использовать
дополнительным внешним линтером; наличие config не равно выполненному запуску.
