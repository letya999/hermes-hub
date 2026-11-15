---
description: "M31: frozen requirements for catalog release 5 — toolset dictionary, via/executor routing, audiences, fail-closed rights, envelope and verification suite."
last_verified: "2026-11-15"
frozen: "2026-11-03"
---

# SPEC-0093: Consolidated model tool surface (catalog r5)

Замороженные требования майлстоуна M31. Решения: ADR-0094. Нормативный
чартер: docs/tool-design.md. Приёмка по ишьюзам #437–#448 (GitHub
milestone "M31 — Consolidated Model Tool Surface (catalog r5)").

## R1. Словарь (M31.0–M31.1)

- R1.1 `platform/catalogs/5/` собирается из тех же Go-источников, что
  исполняют контракт; `toolsets.yaml` содержит ровно словарь ADR-0094 D1.
- R1.2 Каждый тулсет несёт `executor`; каждый `ToolSpec` — `Audience` и
  `Executor`. Неконсистентная пара `via`/`executor` — ошибка сборки.
- R1.3 Не-`model` инструмент не может быть членом тулсета, не появляется в
  `tool_search` и в прямых схемах; исполняется через ToolHub.
- R1.4 `mcp-raw` тулсеты отвергаются в `core`; `scope`/`rules` на
  `mcp-raw`/`native` отвергаются компилятором.
- R1.5 `rights.yaml` r5 содержит домены `sessions`, `tasks`, `ssh`,
  `exposure`, `research`, `browser`, `platform`, `models`; каждый член —
  ≥1 `requires`, резолвящийся в реальное право; без `requires` сборка падает.
- R1.6 `grants.yaml` выражает каждое новое право; грант вне
  issuers/scope — ошибка компиляции.

## R2. Контракт инструментов (M31.2)

- R2.1 Ни одна model-видимая схема не содержит JSON-encoded строковых
  параметров; `plan`, `args`, `sources`, `steps` типизированы.
- R2.2 MCP-аннотации выводятся из `Effect`; конверт результата един;
  коды ошибок — только из закрытого словаря.
- R2.3 Паритет `Server()`↔каталог: нулевой дрейф имён, описаний, схем.

## R3. Семейства (M31.3–M31.8)

- R3.1 Все 88 model-инструментов существуют под именами r5; старые имена
  отсутствуют на model-поверхностях; rename-map покрывает каждый старый id.
- R3.2 `file_edit` — точечные правки с revision CAS; `terminal_exec` —
  bounded output + `job_id`; `ssh_shell` сессии переживают вызовы;
  `session_*` без SessionStore отвечает `unavailable`.
- R3.3 Каждое семейство исполняет своё право per-call, включая `tool_call`;
  `browser_*`/`research_*` честно отвечают `unavailable` без бэкенда.
- R3.4 `platform` всегда в core; `tool_search` ищет по описанию и
  параметрам; `platform_bootstrap` идемпотентен после завершения.

## R4. Проводка (M31.9)

- R4.1 `tools.yaml` принимает логические имена; двусмысленное имя без `via`
  — понятная ошибка компиляции.
- R4.2 Прямой вызов и `tool_call` одной пары `(tool,args)` дают одинаковые
  результаты и отказы; отзыв гранта бьёт следующий вызов.
- R4.3 Промпт не упоминает выключенные семейства; включение `browser`
  добавляет ровно секцию browser.
- R4.4 Executor за PEP не расширяет права сверх проекции; uncurated raw MCP
  никогда в core и без scope-претензий.

## R5. Релиз и верификация (M31.10–M31.11)

- R5.1 Каждый каталогизированный инструмент есть в docs/tools.md под
  r5-именем; denied-by-design список документирован с причинами.
- R5.2 Legacy `tools.yaml` мигрирует или падает с actionable hint;
  манифест r5 верифицируется по дайджестам.
- R5.3 Линты/паритет/контракт-тесты бегут в `just check` и роняют сборку;
  eval-сьют даёт повторяемый отчёт с r5-базой.
- R5.4 Таблица соответствия в tool-design.md мапит каждое правило на тест.
