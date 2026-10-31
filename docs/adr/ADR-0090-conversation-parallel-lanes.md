---
description: "Параллельные сессии внутри одного контекста: lane-ключ (principal, context, conversation), fairness по контексту, per-context cap ниже upstream-лимита. Амендит ADR-0025."
last_verified: "2026-10-30"
---

# ADR-0090: Параллельные сессии внутри контекста (conversation lanes)

Status: accepted; implementation in CHG-0234.
Дата решения: 2026-10-30.

Связи: [ADR-0025](ADR-0025-context-parallel-gateway-workers.md) (уточняется,
не переписывается), [SPEC-0012](../../specs/active/SPEC-0012-scale-to-zero-runtime-lifecycle.md)
правило 4, [ADR-0074](ADR-0074-session-runtime-discipline.md) D4,
[SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md),
[SPEC-0089](../../specs/active/SPEC-0089-conversation-parallel-lanes.md),
[план](../../.work/in-progress/CHG-0234-conversation-parallel-lanes/plan.md).

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
