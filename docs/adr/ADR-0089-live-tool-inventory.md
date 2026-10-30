---
description: "MVP: живой caller-scoped инвентарь инструментов — tools_list + invoke вместо статичного промпт-каталога, deferred hydration или ToolHive vMCP."
last_verified: "2026-10-30"
---

# ADR-0089: Живой инвентарь инструментов в сессии

Status: accepted; implementation in CHG-0233.
Дата решения: 2026-10-30.

Связи: [SPEC-0023](../../specs/active/SPEC-0023-toolhub-control-plane.md),
[план](../../.work/done/CHG-0233-live-tool-inventory/plan.md),
ADR-0024 (ToolHive vMCP — не авторизационная граница), SPEC-0053, docs/rbac.md.

## Контекст

Коннекторная поверхность может вырасти до сотен инструментов. Модель в живой
сессии не может ответить «что у меня есть» без вызова `system_guide` (тяжёлый
статический документ) или перечитывания `tools/list`, который на клиенте
устаревает до reconnect — даже когда `RefreshProjection` уже обновил сервер.
Коннектор, включённый в этой или соседней сессии, виден в сторе немедленно,
но его точное имя недоступно модели.

Исследование harness-ов (eve catalog: `eve__search`/`eve__tool` + announcements;
goclaw: пороговый дефер + `mcp_tool_search` + lazy-активация в registry;
nanoclaw: respawn контейнера на `add_mcp_server`; OpenDots: полный inline;
open-dot: хостедный Composio search) сходится на паттерне
«discovery-инструмент + dispatch по имени». Управление лупом у goclaw даёт
гидратацию схем — недоступную за MCP-границей Hermes; eve в той же ситуации
выбирает dispatch-инструмент — та же развилка, что и у нас.

## Решения

### D1. Живой инвентарь — это tool result, не промпт-блок

`soul-effective` отрендерен до спауна и уже в контексте: переписывание файла
не обновляет сессию. Единственный канал до живой сессии — ответ инструмента.
Статичный `<available_tools>` блок не вводится: он устаревает внутри сессии
и тратит токены каждый ход. В промпт добавляется только одна строчка-указатель.

### D2. `tools_list` читает тот же путь, что и `tools/list`

Новый ToolHub-инструмент регистрируется безусловно рядом с `system_guide` и на
каждый вызов читает `ListProjectedTools(auth)` + `filterProjectedTools` —
идентичные входы проекции. Дрейфа ноль: включённый в любой сессии коннектор
виден сразу; отзыв пропадает на следующем вызове. Caller-scoping и
governance-фильтр наследуются, а не дублируются. Инструмент отдаёт только
собственную admitted-поверхность звонящего — как `tools/list` — поэтому грант
не требуется.

### D3. `invoke` — dispatch-путь для имён вне кэша клиента

Существующий `invoke` (control_http.go) уже вызывает проецируемый инструмент
по имени через `g.call`: admission, схема, инъекция, аудит идентичны прямому
вызову. `invoke` не расширяет авторизацию — он снимает зависимость от кэша
`tools/list`. Под managed-профилями `invoke` едет составом connectors-бандла
(`personal` / `personal+team`); `connectors: ""` сохраняет строгий
`control_operations`-аллоулист, `connectors: none` и legacy-путь не меняются.

### D4. Компактный вывод по умолчанию

Дефолтный ответ — `{name, definition, effect}` без описаний и схем (урок
eve-листинга: перечень не называет сигнатур). `query` (подстрока, ≤256) и
точный `name` возвращают полный spec — describe бесплатно тем же инструментом.
`limit` ограничивает вывод (дефолт 100, max 200). Ответ честно обозначает
границу: `mcp__hub__*` и `skills_list` живут на другом сервере.

### D5. Отклонённые альтернативы

- Deferred hydration / нативная подмена tools/list — требует владения лупом
  (upstream Hermes); `invoke` — эквивалентный dispatch на нашей границе.
- ToolHive vMCP как авторизационная граница — запрещено ADR-0024 (stale
  backend addressing, anonymous quick-group); ToolHive остаётся раннером.
- Catalog-change announcements через continuation-job канал — инфраструктура
  есть (`deliverPrepareNotice`), но фан-аут по сессиям — отдельная задача
  после MVP.
- BM25/индексный поиск — не нужен при сотнях; `query` покрывает фильтрацию.

## Нецели

`tools_list` не объединяет `mcp__hub__*` нативную поверхность (другой сервер),
не заменяет `skills_list` и не меняет readiness/`tool-manifest.json`
(surface.go сверяет `/v1/connectors`, не tools/list). Никаких изменений
MCP-протокола и provider API.
