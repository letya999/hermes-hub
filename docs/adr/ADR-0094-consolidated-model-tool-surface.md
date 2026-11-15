---
description: "M31: consolidated model tool surface — catalog release 5 with 20 first-party families, via/executor two-level routing, model|internal|operator audiences and logical toolset names."
last_verified: "2026-11-08"
---

# ADR-0094: Consolidated model tool surface (catalog r5)

Status: accepted direction. Дата решения: 2026-11-03.
Уточняет [ADR-0092](ADR-0092-six-passport-space-contract.md) D6 (язык тулсетов)
и [ADR-0093](ADR-0093-imperative-passports-capability-grants.md) D3/D6.
Нормативный чартер: [tool-design](../tool-design.md).
Требования: [SPEC-0093](../../specs/active/SPEC-0093-consolidated-model-tool-surface.md).

## Контекст

Каталог r4 вырос эволюционно: 70+ инструментов с шестью глаголами удаления,
JSON-in-string параметрами, `tools.select`-фолбэком для ssh/exec/tasks,
одиннадцатью `assignment_*` инструментами и параллельными рукописными
схемами в `Server()`. Чартер `docs/tool-design.md` фиксирует целевой
контракт; этот ADR фиксирует словарь релиза 5, который чартер делает
обязательным.

## Решения

### D1. Замороженный словарь r5

`platform/catalogs/5/toolsets.yaml` содержит ровно:

- 20 first-party семейств под **логическими именами** (без `hub:`):
  `files`, `terminal`, `documents`, `media`, `skills`, `routines`,
  `watches`, `assignments`, `goals`, `context`, `settings`, `ssh`,
  `exposure`, `sites`, `web`, `browser`, `research`, `connectors`,
  `models`, `sessions`.
- `platform` — pinned-тулсет, всегда в core, не выбирается оператором.
- 25 `native:` carveout, 3 `native:` pseudo, ~37 `native:` denied-by-design
  (инвентарь минус carveouts; без секции и без `via`).
- Connector-тулсеты: `github`, `gitlab`, `google`, `slack`, `atlassian`,
  `telegram_user`, `desktop`, `drafts` (kind `connector`, via `mcp`,
  curated); `user_mcp`/`org_mcp` (kind `connector`, via `mcp-raw`,
  uncurated); bounded CLI cells (`cli-*`, kind `cli`).
- Удалены `hub:archives`, `hub:artifacts`, `hub:inbox`, `hub:knowledge`,
  `hub:hh`, `hub:services`, `hub:skills_manage`, `hub:hosting`, `hub:tools`,
  `hub:images`, `hub:image_gen` — поглощены семействами или ушли в рецепты.

88 model-инструментов + `inbox_fetch` (internal) + 13 control ops (operator).

### D2. Двухуровневая маршрутизация `via`/`executor`

`via` — маршрут доставки и admission (`native`, `toolhub`, `mcp`, `mcp-raw`,
`off`), как в r4. `executor` — кто исполняет вызов за PEP:
`handler:<family>` (in-runtime agenttools), `connector:<name>`,
`flow:<name>`, `runtime:<api>` (hub-side backends — `runtime:sessions`,
`runtime:toolhub`, `runtime:connectors`), `native`, `off`. `via: native` ⇒
`executor: native`; `via: mcp` ⇒ `connector:*`; неконсистентная пара —
compile error и catalog test.

### D3. Audience

`ToolSpec.Audience ∈ model|internal|operator`. `inbox_fetch` → `internal`;
13 legacy control ops (`discover`…`install_shared`) → `operator`.
Не-`model` инструменты не являются членами тулсетов, не попадают в
`tool_search`/прямые схемы, но исполняются через ToolHub.

### D4. Права fail-closed

Каждый член несёт `requires` на домены `rights.yaml` r5 (расширены:
`sessions`, `tasks`, `ssh`, `exposure`, `research`, `browser`, `platform`,
`models`). Фолбэк `tools.select` удалён: член без маппинга — ошибка сборки
каталога, не предупреждение.

### D5. Логические имена + via в tools.yaml

`tools.yaml` выбирает логические имена; `hub:`/`native:` префиксы — только
миграционный вход. Имя, покрытое двумя тулсетами (`terminal`, `web`,
`browser`, `skills`, `files`↔`file` и т.п.), требует `via`; неоднозначность
без `via` — compile error. `platform` добавляется компилятором в core и
не может быть выбран/удалён.

### D6. Один исполнительный путь, единый конверт

`ExecCall` остаётся единственным путём; `tool_call` (was `invoke`) и прямой
вызов идентичны по admission и результату. Конверт
`{ok, data, error?:{code,message,retryable,hint}}` с закрытым словарём
кодов — норматив для всех поверхностей.

## Последствия

- Каталог r5 иммутабелен: заморожены имена тулсетов, членов, capability id,
  прав и executors. Схемы и описания — в Go, вне эмитируемых байт.
- Старые имена — только в rename-map (tool-design.md §migration) и в
  миграторе; обратных шимов в исполнении нет.

## Addendum (2026-11-03): typed schemas, modular contract, fail-closed requires

Реализация M31 зафиксировала три уточнения, не меняющие замороженный
словарь r5:

- **Typed arguments (definition 7.0.0).** `CLIArgument` несёт `description`,
  `enum` и `items`; `CliInputSchema` всегда эмитит
  `additionalProperties: false`, включая инструменты без аргументов.
  Структурные параметры (`args`, `sources`, `needs`, `steps`, `extract`,
  `plan`) типизированы `object`/`array` — wire-форма совпадает со схемой;
  внутренние `Input`-поля принимают `any`, и `jsonParam`/`hasParam`
  декодируют обе формы (объект и legacy JSON-строку) одинаково.
- **Fail-closed requires.** `rightForCap` не имеет фолбэка на
  `tools.select`: незамапленный capability id даёт пустое право, а
  `annotateToolsets` ломает генерацию каталога с перечнем членов.
- **Uncurated never in core.** `user_mcp`/`org_mcp` (и любой тулсет с
  `uncurated: true`) демотируются компилятором из `core` в `extended`
  до обработки бюджета.
- **Modular channel contract.** Контракт — композиция секций по
  семействам; секция рендерится, пока у семейства нет явного `via: off`.
  `Settings{}` воспроизводит полный контракт, поэтому ungoverned-спейсы
  не теряют гайд.
- **Closed diagnostics.** `platform_status` эмитит ровно четыре
  allowlisted поля; коннектор-инструменты не объявляют аргументов
  формы credential — секреты ходят только через защищённую форму.

## Дополнение 2026-11-08: каталог 21

В управляемом режиме семейство `web` переименовано в `web_search` без
переименования инструмента `web_search`. Выбор провайдера остаётся в
опубликованной конфигурации, а вызов проходит через ToolHub к приватному
исполнителю закреплённого Hermes runtime. `browser_*` также выполняются
через ToolHub и приватный tools-daemon, который обращается к существующему
Playwright sidecar; прямой model-visible MCP server для browser убран.
Опциональные companion-пункты `desktop` и macOS `drafts` удалены из нового
каталога; `native:desktop_ui` и черновики workspace не меняются. Каталог 20
остаётся неизменным для воспроизведения прежних ревизий.
