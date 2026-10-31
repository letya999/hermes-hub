---
status: frozen (supersedes SPEC-0012 rule 4)
title: Parallel session lanes per context
---

# SPEC-0089: Параллельные сессии внутри контекста (conversation lanes)

Frozen: 2026-10-30, после принятия ADR-0090 владельцем.

Supplements SPEC-0012 (правило 4), SPEC-0013, SPEC-0053 — без переписывания
их истории. Design: [ADR-0090](../../docs/adr/ADR-0090-conversation-parallel-lanes.md).
Implementation plan: [CHG-0234](../../.work/in-progress/CHG-0234-conversation-parallel-lanes/plan.md).

Доказательство гейта SPEC-0012 правило 4 («later concurrency must prove
Hermes session and filesystem safety»): по пину `869228cab` каждый
`/v1/runs` исполняется в своём executor-потоке с per-run contextvars и
WAL-состоянием; транскрипты разделены по `session_id`; per-session lock в
api_server отсутствует — поэтому хаб сохраняет сериализацию *внутри*
сессии и снимает её *между* сессиями. Filesystem safety — принятый
last-writer-wins предел R8, задокументированный, а не скрытый.

## Requirements

- **R1.** Ключ сериализации gateway — `(principal_id, context_id,
  conversation_id)`. Пустой `conversation_id` сводится к парному ключу по
  контексту; rebuild inFlight и requeue не теряют держателей.
- **R2.** Разные сессии одного контекста исполняются конкурентно до лимита
  R3; сообщения внутри одной сессии — строго FIFO.
- **R3.** Per-context admitted cap `HUB_CONTEXT_MAX_PARALLEL`, default 8,
  обязан быть ниже upstream `gateway.api_server.max_concurrent_runs`
  (default 10). Контекст на лимите пропускается при claim — задача ждёт,
  не отклоняется.
- **R4.** Fairness: курсор ротации и сортировка claim остаются по
  `(principal_id, context_id)`; burst сессий одного владельца не вытесняет
  чужие контексты.
- **R5.** Глобальный пул воркеров конфигурируется
  `HUB_COMMUNICATION_WORKERS`, default 32.
- **R6.** Pre-admission отказ upstream (включая 429 при заниженном upstream
  cap) — терминальный `failed` с явным событием; durable requeue вне этого
  changeset.
- **R7.** Форумные топики (`mode: topics`) и DM-задачи одного юзера —
  отдельные сессии и lane'ы; доставка и статусные сообщения идут в свой
  топик (`TopicID`/`DeliveryTargetID` неизменны).
- **R8.** Принятые пределы задокументированы и помечены `ponytail:`:
  shared workspace/`MEMORY.md` — LWW; один browser sidecar на space;
  общий blast radius рантайма и общий `consecutiveUncertain`-счётчик.
- **R9.** Регрессионные тесты обязаны покрыть: параллельный claim разных
  сессий одного контекста; FIFO внутри сессии; fairness между контекстами;
  cap-пропуск; rebuild inFlight с новым ключом; uncertain без admission не
  держит lane; пустой `conversation_id` → контекстный ключ.

## Acceptance

- **A1.** Мок-стек: сообщения в 3 разных топика одного юзера → 3 admitted
  run'а одновременно в одном рантайме (пересечение по `run_id`/`session_id`
  в trace/логах).
- **A2.** Два сообщения в один топик → второй run стартует после первого;
  транскрипт не перемешан.
- **A3.** Контекст на cap'е: следующая задача остаётся pending и стартует
  после освобождения; чужой контекст claim'ится.
- **A4.** `just check` зелёный; регрессии R9 входят в suite; покрытие
  ≥85%.
- **A5.** Live dev-stack (форумная группа): 3 реальных сообщения в 3
  топика → параллельные сессии; каждый ответ приходит в свой топик;
  статусные сообщения не смешиваются.
- **A6.** Ни одного `failed` с событием rate_limit/429 при штатном R3 <
  upstream cap.
