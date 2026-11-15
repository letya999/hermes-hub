---
description: "M30: паспорта space — императивный и единственный вход сборки, запуска и admission; capability-гранты агенту как псевдо-RBAC поверх тулсетов."
last_verified: "2026-11-02"
---

# ADR-0093: Императивные паспорта и capability-гранты агента

Status: accepted direction; implementation not started. Дата решения: 2026-11-02.
Уточняет и частично замещает [ADR-0092](ADR-0092-six-passport-space-contract.md)
(D1 «read-only материализация», D2 формы тел, D3 словарь грантов, D6 язык тулсетов).
Требования: [SPEC-0092](../../specs/active/SPEC-0092-imperative-passports-capability-grants.md).
Связи: [RBAC](../rbac.md), [toolhub](../services/toolhub.md),
[model relay](../services/model-relay.md), [ADR-0089](ADR-0089-live-tool-inventory.md),
[ADR-0083](ADR-0083-lean-images-capability-packs.md), [ADR-0086](ADR-0086-memory-vault-openknowledge.md).

## Контекст

M29 (CHG-0241…0253) выпустил компилятор и атомарную публикацию шести
паспортов, но аудит после приёмки (`.work/done/CHG-0254-passport-gap-audit/research.md`)
показал, что контракт декоративен:

- Ни один потребитель не читает опубликованную ревизию. `stack.RenderEnvironment`
  берёт `Settings` из legacy `agent.yaml`/`workspace.yaml` (`ReadEnvironment`),
  compose и spawn супервизора монтируют жёстко заданный список путей,
  model-relay читает registry из `models.yaml`, ToolHub — вручную опубликованные
  capability-записи. Из паспортов читается только `prompt-pointer.txt`.
- Тела паспортов — денормализованные списки имён: `stack.services` без режимов
  и identity, `config` без runtime/ports/supervision/channels, `models` без
  routes/ladder/via/endpoint/auth/limits/models, `workspace` дублирует
  `layout`/`tree` и делает весь `/workspace` writable, `tools.core` — имена
  инструментов вместо тулсетов без `via/tools/scope/rules/access`,
  `grants.allow` пуст.
- «Capability» в каталоге — это id отдельного инструмента (`files.read` ↔
  `file_read`), а не право агента. Словаря фундаментальных прав
  (workspace, memory, models, tools, skills, connectors, agents, users, stack,
  config, channels, observability) нет, поэтому grants нечем наполнить.
- Приёмка 10 спейсов прошла только после ручной сборки ToolHub policy/profile
  из записей `artem_main`: tools.yaml не управлял тем, что видит агент.

## Решения

### D1. Паспорт — императивный вход, не отчёт

Опубликованная ревизия — единственный вход для сборки образов, рендера
compose, spawn runtime, конфигурации сервисов и per-call admission
управляемого space. Каждое поле тела имеет зарегистрированного потребителя
и класс эффекта; поле без потребителя — ошибка схемы (тест реестра
потребителей). Legacy-файлы остаются только входом мигратора. Managed space
без опубликованной ревизии не рендерится и не стартует.

### D2. Формы тел — нормативные примеры SPEC-0092

Файл состоит из короткого `metadata` (space, environment, revision,
generated_at, template, catalogs) и верхнеуровневых ключей контракта без
обёртки `body`. Формы совпадают с нормативными примерами SPEC-0092:
`stack` — `identity` + `services: {name: {mode}}`; `config` — runtime,
supervision, ports, channels, memory; `models` — `default`, `routes.*.ladder`,
`services.*.{via,endpoint,auth,sharing,limits,models}`; `workspace` — один
ASCII `tree`, `writable`, `read_only`, `mounts`; `tools` — только `core` и
`extended` со значениями-тулсетами; `grants` — `allow: {capability: [ops]}`.
Производные поля (зависимости, overflow, деревья) вычисляются, но не
дублируются вторым представлением.

### D3. Capability — право агента, а не инструмент

Вводится словарь capability-доменов с операциями: каждая операция задаёт
субъект (`self` или `cross`), вид scope, допустимого выдающего
(platform/org/owner/operator), точку применения (PEP) и эффект. Тулсеты и
инструменты не являются capability: инструмент объявляет, какие операции
ему нужны (`requires`), и проецируется только при их наличии в эффективных
грантах. Нынешние id уровня инструмента (`files.read` и т.п.) переходят в
каталог тулсетов как внутренние `actions`. Шаблоны не выдают прав;
отсутствующая операция — deny; cross-user операции отдельные.

### D4. Одна точка решения, много точек применения

ToolHub держит единственный PDP: доверенный ledger грантов, потолки
platform/org и revocation epoch. PEP вызывают его на каждом действии:
ToolHub call admission (direct и invoke), файловые инструменты (пути),
model-relay (list/use/switch), selfsettings (config.read/edit_own),
supervisor (agents.*), communication (channels.*), диагностика
(observability.*), hubctl (stack.inspect). `grants.yaml` — снимок для чтения;
ручная правка снимка ничего не меняет.

### D5. Классы эффекта и reconcile без перезагрузки сессии там, где это доказуемо

`hubctl space apply` компилирует, показывает diff с классом эффекта каждого
изменения, публикует ревизию и запускает reconcile:

- `live` — без рестарта: ToolHub-проекция (`via: toolhub`/`mcp`) и
  `notifications/tools/list_changed` в текущей сессии; writable/read_only
  проверяются файловыми PEP по текущей ревизии; маршруты, лимиты и модели
  перечитываются model-relay; гранты — по epoch.
- `respawn` — следующий turn в новом runtime: native-тулсеты Hermes,
  mounts и дерево, runtime-настройки.
- `recreate` — compose пересоздаёт сервис: порты, каналы, auth-контракт
  сервиса.
- `rebuild` — `hubctl build` и пересоздание: состав и режим сервисов stack.

Изменения, класс которых не доказан тестом и живой проверкой, помечаются
`respawn`, а не `live`.

### D6. Stack и workspace определяют контейнеры

`stack.services` с режимами `supervised`/`shared`/`per-space` — это план
compose-проекта и spawn: per-space экземпляры (включая несколько
`cliproxy-*`) рендерятся как контейнеры space, shared потребляются с infra,
supervised создаёт supervisor. Имя экземпляра `<catalog-id>[-<suffix>]`.
`workspace` — это план монтирования и материализации каталогов, по которому
compose и supervisor создают контейнер runtime: `/config` (паспорта,
system-prompt, SOUL/USER/AGENTS), `/workspace/*`, `/vault`, `/skills`,
`/shared/*`. Каталог монтируется директорией, не одиночным файлом, чтобы
атомарная замена ревизии была видна внутри контейнера.

### D7. Tools — язык тулсетов

`core` и `extended` содержат тулсеты (`files`, `skills`, `ssh`,
`connectors`, `watchers`, `browser`…), значение — `via`, `tools.include`
или `tools.exclude`, `access`, `scope` (`paths`/`hosts`/`urls`) и
`rules.<tool>.scope`. `discovery` (system_guide, tools_list, invoke) всегда
в core. Пользователь предлагает core, компилятор переносит переполнение
целым тулсетом в extended в порядке каталога. ToolHub-профиль и политика
выводятся из ревизии автоматически при apply (с подтверждением владельца/
оператора); ручные capability-записи для managed space не нужны.
Неисполнимые ограничения для `native`/`mcp-raw` отклоняются компиляцией.

### D8. Models управляет сессиями авторизации через Broker

Сервис модели = один credential principal. `via: cliproxy` требует
одноимённого per-space экземпляра в stack; `auth.upstream` (oauth-session
или broker-key) доставляется Broker-контрактом только в этот экземпляр,
`auth.proxy_access` — ключ, которым model-relay ходит в cliproxy. Логин
OAuth-сессии выполняется через существующий механизм cliproxy под
контролем hubctl и сохраняется в Broker, не копированием auth-каталогов.
model-relay рендерит registry из паспорта: маршрут → лестница сервисов →
модели сервиса; лимиты `{of, amount, unit?, per, clock?}`; переключение
маршрута требует `models.switch`. Лимит, который relay не может измерить
(provider_quota без телеметрии, spend_usd без прайса), не активирует сервис.

## Альтернативы

- Оставить паспорта отчётом и чинить legacy-рендер: сохраняет два источника
  правды — отвергнуто.
- Сделать capability = тулсет: смешивает «что агент видит» и «что агенту
  разрешено», не покрывает model switch, self-restart, voice, logs —
  отвергнуто.
- Перегружать runtime на любое изменение: проще, но ломает живую сессию и
  противоречит ADR-0089 — используется только для доказанно не-live полей.

## Инварианты

Upstream Hermes не патчится; серверная авторизация не доверяет промпту и
снимку; секреты не попадают в паспорта, argv, DTO и логи; Broker не
становится раннером или каноническим хранилищем; dev/prod изолированы;
M20 владеет внутренностями vault, M27 — packs, M28 — shared edge.
