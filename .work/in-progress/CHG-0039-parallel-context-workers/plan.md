---
description: "Параллельное исполнение заданий communication-gateway между контекстами без взаимной блокировки пользователей."
last_verified: "2026-09-24"
---

# CHG-0039 Parallel per-context gateway workers

## Root cause

Gateway исполняет задания одним worker'ом (`Gateway.Run` запускает один
`g.worker`), который блокируется на весь `runner.RunOutcome`. Долгая задача
одного пользователя (наблюдалось: MCP-онбординг local, ~5 мин) останавливает
очередь для всех остальных — job'ы test-owner-2 лежали в `pending`, пока
running занят чужим заданием. ADR-0007 фиксировал «one worker, global
concurrency one» как ограничение первого релиза; SPEC-0007 явно допускает
«later worker concurrency».

## Design

Per-key dispatch (analog Temporal FairnessKey / SQS MessageGroupId):
единая FIFO-очередь `pending/` сохраняется; ключ сериализации —
`(principal_id, context_id)`, зеркало supervisor `runtimeKey` (runtime_mode
всегда gateway на этом контуре).

- `Spool.inFlight map[ctxKey]jobID` (in-memory, под `s.mu`).
- `ClaimJob` сканирует `pending/` по порядку и пропускает кандидатов, чей
  контекст уже занят другой джобой; занятый контекст может клеймить только
  та же джоба (requeued observation после uncertain/restart).
- Освобождение централизовано в `updateMappingLocked`: терминальный статус
  (completed/failed/cancelled/interrupted) удаляет ключ. `uncertain` ключ
  не снимает — исходный run может ещё исполняться (fail-closed).
- `rebuildMappings` наполняет `inFlight` из не-терминальных маппингов
  существующих job-файлов (pending/running) при рестарте.
- `Gateway.Run` поднимает `cfg.Workers` горутин worker'а; deliverable/requeue
  остаются в отдельных горутинах (delivery уже выделена; RequeueObservations
  переезжает в control-цикл — единый вызывающий).
- `Config.Workers`, env `HUB_COMMUNICATION_WORKERS`, default 4
  (≤ supervisor `--max-concurrent` 8). Нулевое значение в тестах = 1
  (старое поведение).
- `busy` → `atomic.Int32` (число активных run'ов); `/status` отвечает
  «обрабатывает» при >0.

## Scope

- `internal/communication/communication.go`: Spool.inFlight, ClaimJob scan,
  worker pool, control-loop RequeueObservations, busy counter, Config.Workers.
- `internal/communication/job_mapping.go`: release hook в updateMappingLocked.
- `internal/communication/parallel_test.go`: regression tests.
- docs: ADR-0025, SPEC-0007 out-of-scope поправка, operations.md knob.

## Non-goals

- Параллельность внутри одного контекста (SPEC-0012 п.4 — до доказательства
  Hermes session/fs safety).
- Веса/fairness между контекстами, per-user лимиты, БД-очередь.

## Risks

- Залипший uncertain держит свой контекст до терминала (как сегодня, но
  только для своего) — наблюдаемо через mapping/ObservationAttempts.
- pending-скан на каждый клейм — O(files); объём single-host мал.
- workers>supervisor max-concurrent просто ставит Acquire в очередь семафора —
  безопасно, но бессмысленно; default 4 документирован.

## Verification

- go test ./internal/communication (новые тесты + существующая регрессия)
- just check; живой прогон двух telegram-пользователей на dev-стеке.
