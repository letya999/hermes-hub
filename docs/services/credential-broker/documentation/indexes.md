---
description: "Генерация аннотированных таблиц без ручного дублирования."
last_verified: "2026-10-20"
---

# Индексы и метаданные

Каждый Markdown-файл содержит ровно quoted description и last_verified в YAML frontmatter.
Один H1. Индекс состоит только из frontmatter, H1 и таблицы непосредственных дочерних документов.
Пути и описания берутся из реальных файлов. Генерировать `just docs-fix`; stale index блокирует check.

В индексе нет фиктивного README для пустой папки artifacts. Индексы генерируются для зон
этого дерева (`docs/services/credential-broker/*/`); корневые `docs/adr`, `specs` и `.work`
индексирует общий `devcheck docs`. Оглавления отдельных документов не дублируются.
