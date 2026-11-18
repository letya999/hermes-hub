---
description: "Параллельные сессии внутри одного контекста: lane-ключ (principal, context, conversation), fairness по контексту, per-context cap ниже upstream-лимита. Амендит ADR-0025."
last_verified: "2026-11-09"
---

# ADR-0090: Параллельные сессии внутри контекста (conversation lanes)

Status: accepted; implementation in CHG-0234.
Дата решения: 2026-10-30.

Связи: [ADR-0025](ADR-0025-context-parallel-gateway-workers.md) (уточняется,
не переписывается), [SPEC-0012](../../specs/active/SPEC-0012-scale-to-zero-runtime-lifecycle.md)
правило 4, [ADR-0074](ADR-0074-session-runtime-discipline.md) D4,
[SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md),
[SPEC-0089](../../specs/active/SPEC-0089-conversation-parallel-lanes.md),
[план](../../.work/done/CHG-0234-conversation-parallel-lanes/plan.md).

## Контекст

ADR-0025 распараллелил gateway между контекстами, оставив сериализацию по
`(principal_id, context_id)`. SPEC-0012 правило 4 прямо разрешает пересмотр:
«The initial implementation serializes jobs within one context. Later
concurrency must prove Hermes session and filesystem safety before changing
this rule.» Владелец хочет несколько одновременных сессий в одном space —
топики форума (`mode: topics`, уже mint'ит сессию на топик) и несколько задач
в DM — без ожидания друг друга.

Доказательства по пину `869228cab` (upstream 0.21.0, прочитано в живом образе):

- `gateway/platforms/api_server_runs.py`: каждый `/v1/runs` — asyncio-task
  через `run_in_executor` (свой поток), identity через contextvars
  («concurrent runs must not share identity», ~строка 482); состояние —
  `state.db` в WAL-режиме. Разные `session_id` исполняются конкурентно.
- Глобальный потолок `gateway.api_server.max_concurrent_runs` (default 10,
  `hermes_cli/config_defaults.py:1976`): сверх него HTTP 429 до admission;
  очереди ожидания в пине нет.
- Per-session lock в api_server отсутствует: `gateway/wake.py:24` —
  «The API server has no per-session lock (concurrent turns are
  last-writer-wins)». Turn leases живут в messaging-пути upstream. Значит
  сериализацию внутри одной сессии обязан сохранять хаб — и это же
  доказывает, что сериализация *между* сессиями хабу не нужна для
  корректности транскриптов (они разделены по `session_id`).

Конкуренты (прочитаны по коду/докам): openclaw — session-key
`…:group:<id>:topic:<threadId>` + mailbox на сессию + global lane; goclaw —
`BuildGroupTopicSessionKey` + `SessionQueue` на ключ + lane-semaphores;
nanoclaw — `session_mode: shared`, одна папка агента, разные сессии. Модель
везде одна: FIFO внутри сессии, параллель между сессиями, fairness по
тенанту, глобальный cap.

## Решения

### D1. Lane-ключ — `(principal_id, context_id, conversation_id)`

`inFlight` и пропуск busy-ключа при claim переходят на тройной ключ. Пустой
`conversation_id` (legacy/jobs без conversation) сводится к старому парному
ключу по контексту — совместимость со спулом и rebuild сохраняются. FIFO
внутри сессии неизменен: два сообщения в один топик идут строго по очереди.

### D2. Fairness остаётся по `(principal_id, context_id)`

Курсор ротации и сортировка кандидатов (`spool.go:1069-1082,1116`) не
меняются и не получают conversation-измерение: юзер с 10 топиками не
отбирает пропускную способность у других владельцев — сессии одного scope
делят его квант по обычному FIFO/aging.

### D3. Per-context admitted cap `HUB_CONTEXT_MAX_PARALLEL` (default 8)

Claim пропускает контекст, у которого занято ≥ лимита lane'ов — так же, как
busy-ключ: задача ждёт, не отклоняется. Значение обязано быть ниже upstream
`max_concurrent_runs` (10): 429 до admission в штате недостижим. Если
upstream-конфиг занижен ниже нашего cap, pre-admission отказ завершает job
как `failed` с явным событием — bounded и видимо; durable requeue —
отдельное решение, не этот changeset.

### D4. Дефолт `HUB_COMMUNICATION_WORKERS` 4 → 32

Воркеры — горутины, держащие HTTP-стрим; 4 глобально душили бы новые lane'ы.
Суммарный параллелизм = min(workers, сумма per-context cap'ов). Supervisor
`--max-concurrent` (8 рантаймов) не трогаем — он про контейнеры, не про
сессии.

### D5. Принятые пределы (явно, не маскируются)

- Общий workspace и `MEMORY.md` — last-writer-wins между параллельными
  сессиями (тот же компромисс, что у openclaw/goclaw/nanoclaw shared-mode).
- Один browser sidecar на space: две сессии с браузерными задачами делят
  один ресурс.
- Blast radius рантайма общий: рестарт/unclean exit контейнера задевает
  все его сессии; `consecutiveUncertain` wedge-счётчик тоже общий.
- Каждый пункт помечается `ponytail:`-комментарием в коде с путём
  обновления (per-session workspace, browser lane, per-session wedge).

### D6. Никаких новых абстракций

Не вводятся lane-registry, очереди на сессию, отдельные store. `inFlight`
уже `map[key]jobID` — добавление измерения в ключ покрывает требование
целиком.

## Отклонённые альтернативы

- **Контейнер на сессию** (nanoclaw isolation level 3): многократная
  стоимость compute; Hermes уже мультиплексирует сессии в одном процессе,
  а scale-to-zero гасит простаивающие контексты.
- **Конкурентные run'ы внутри одной сессии** (goclaw `maxConcurrent>1`):
  upstream-транскрипт под конкурентными писателями — last-writer-wins
  (доказательство выше), дубли ответов и `user;user`-wedge; запрещено D1.
- **Delegated-run / subagent lanes** (goclaw-rd модель): отдельная фича,
  не отвечает на «несколько сессий в топиках»; позже delegated-job просто
  получит свой conversation и ляжет в ту же модель.
- **Ожидание upstream run-queue** (upstream PR #118555): пин очереди не
  содержит; локальный cap+skip дешевле бампа зависимости.

## Инварианты

- FIFO, идемпотентность и durable-маппинги per conversation неизменны;
  ключ держит только admitted run — `uncertain` без admission не блокирует
  lane (инвариант ADR-0025 сохранён).
- Hermes session/run API неизменен; upstream не патчится.
- Авторизация, групповые allowlist'ы, delivery-target binding и аудит не
  затронуты; запись job/mapping/lease — по-прежнему durable до dispatch.
- Глобальный параллелизм ограничен workers и per-context cap; upstream cap
  не превышается никогда.

## Дополнение 2026-11-09: граница MCP-корреляции M32

После переноса memory tools в ToolHub выяснилось, что статический MCP-клиент
Hermes не передаёт job/run identity отдельного вызова. Runtime привязывает
MCP-прокси к одной подтверждённой паре job/run. Поэтому D1–D4 продолжают
описывать очередь gateway, но не доказывают одновременное исполнение двух
managed run внутри одного контейнера. Параллельный admission раньше давал
отказ второго run до получения run ID; runtime теперь ждёт освобождения
активной привязки. Полноценная параллельность managed-сессий остаётся
неудовлетворённым требованием SPEC-0089 и требует раздельной MCP-идентичности
или изолированных runtime с сохранением истории каждой сессии. Не следует
снимать блокировку общего прокси: это смешает авторизацию и аудит разных run.

## Дополнение 2026-11-09: пять изолированных managed runtime

Предыдущее дополнение зафиксировало причину отказа, а не окончательное
решение. Supervisor теперь выделяет одному владельцу до пяти фиксированных
runtime-слотов. Каждый слот содержит отдельный процесс Hermes и отдельный
MCP-прокси с точной привязкой к job/run; шестой job ждёт свободного слота.
Сообщения одной conversation остаются последовательными. Режим слота
сохраняется в durable job record и применяется при повторном чтении статуса,
управлении и восстановлении. История Hermes общая для слотов владельца,
служебный `/state` отдельный. Подготовка общего generated config/skills
выполняется под блокировкой до создания контейнера; модельные run'ы
исполняются без этой блокировки.

Для managed execution это уточнение заменяет реализацию D3, D5 и D6:
предел — пять, контейнер и MCP identity отдельны, а общий workspace и
browser sidecar остаются общими. Общий host cap supervisor по умолчанию
увеличен до 16 контейнеров; оператор может его уменьшить. Подтверждение
одновременного выполнения и общего источника истории записывается в CHG-0288.

## Дополнение 2026-11-09: один процесс с идентичностью каждого run

Предыдущее решение о пяти контейнерных слотах отменяется для новых managed
job: оно доказало параллельность, но на холодном старте создаёт до пяти Hermes
runtime и пяти control relay. Свежие изолированные пробы этой схемы завершились
отказом supervisor 503 при создании runtime (189,54 с и 97,42 с). Выборочный
срез `docker stats` в первом прогоне достиг 372,83 MiB для двух runtime и
четырёх relay; это не пик всех десяти контейнеров. Ранее тот же тест прошёл
за 88,90 с. Эти данные требуют сравнения с новой схемой на том же тесте.

Hermes остаётся на пине `869228cab`. Явный patch
`docker/patches/hermes-mcp-run-identity.patch` применяется с `git apply --check`
при сборке и использует существующий per-run approval context upstream:
в `/v1/runs` его session key равен run ID. Только вызовы `toolhub` получают
этот ID в MCP `tools/call.params._meta`; остальные MCP серверы не меняются.
ToolHub работает через stateless `/mcp-run`, поэтому его вызовы не держат
общий MCP RPC lock. Go runtime хранит одновременно несколько подтверждённых
пар run→job, отклоняет неизвестный/завершённый ID и не доверяет присланным
job/run HTTP заголовкам. ToolHub независимо проверяет точную пару и текущие
права в supervisor. Supervisor направляет новые managed job одного владельца
в один `gateway` runtime, ограничивая пятью активными conversation и сохраняя
FIFO внутри каждой. Существующие записи `gateway-2`…`gateway-5` остаются
маршрутизируемыми до завершения/восстановления.

Один процесс уменьшает стоимость контейнеров и холодного старта, но расширяет
общую область отказа: падение Hermes затрагивает все пять сессий, а код,
исполняемый внутри одного процесса, не изолирован между ними. Доверенная
граница авторизации остаётся в Go runtime, ToolHub и supervisor; эта схема не
обещает защиту от полного компрометирования самого процесса Hermes.

Изолированный живой прогон новой схемы прошёл четырежды. Финальный полный тест
занял 63,60 с; пять модельных вызовов и записей были готовы за 24,377 с.
При удержании пяти run один runtime занимал 219,2 MiB, control relay —
8,996 MiB. Шестая сессия ждала освобождения слота. Журнал и аудит подтвердили
пять разных точных job/run для записи; Hermes сам выполнил по одному
`memory_search` для каждого из пяти run. Фиксированный порт 8090 старого
тестового окружения пересекался с уже запущенным ToolHub и давал ложный 401/403;
тест теперь использует отдельный свободный порт, не меняя рабочий сервис.
По числу контейнеров (два вместо десяти), измеренной памяти и подтверждённой
параллельности выбран один Hermes процесс. Сравнение 63,60 с с прежними
88,90 с является ориентиром: прогоны сделаны в разных локальных условиях.
