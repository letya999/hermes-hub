# CHG-0222: Один receipt для результата, доставки и обработки source batch

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R4](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0221. Next: CHG-0223.
Hard dependencies: CHG-0216, CHG-0221.

## Goal

Один receipt для результата, доставки и обработки source batch. Результат этапа проверяется acceptance ниже; текущий работающий код
не объявляется соответствующим SPEC до реализации и фактических проверок.

## Read before coding

1. AGENTS.md, README.md, docs/index.md, principles.md, boundaries.md.
2. [Вход и порядок](../CHG-0215-broker-recovery-head/README.md), [общая приёмка](../CHG-0215-broker-recovery-head/acceptance.md),
   [исходные факты](../CHG-0215-broker-recovery-head/research.md); ADR/SPEC по chain.
3. Dependency state.yaml и затронутые current docs. CHG-0214 остаётся отдельной
   незавершённой работой: не reset/stash/commit чужих изменений.
4. Найти callers всех anchors через rg/Serena и прочитать существующие tests.
   Названия новых tests ниже — предложения, не утверждение, что они существуют.

## Anchors и область изменения

- internal/communication/stream.go: streamReceipt, RecordStreamEvent, applyStreamReceiptLocked, recoverStreamEvents
- internal/communication/gateway_delivery.go: terminal branches, retryableJobFailure
- internal/communication/events.go: settleEvents, fanoutEventsLocked; streams.go: settleStreamEvents
- internal/communication/job_mapping.go; spool_delivery.go: atomicJSON, EnqueueDelivery; recovery.go
- internal/toolhub/gateway_call.go: OAuthRefresh retry; internal/runtime/server_observe.go: retryColdWake

## Slices

### S198.1 terminal-receipt

Требования: SPEC-0088 R4; один slice — один проверяемый changeset.

Изменение: Все completion/failure/uncertain paths сохраняют outcome+exact delivery intent до изменения производных mapping/outbox. Переиспользовать streamReceipt, стабильный event/delivery ID. Recovery восстанавливает handoff без resubmit prompt; убрать проигнорированные mandatory errors.

Tests (red first; ожидаемый результат определяется SPEC):
- communication tests: TestTerminalReceiptCrashAtEachHandoffStep; TestRepeatedTerminalReceiptKeepsSingleDelivery; TestFailedOutcomeSaveDoesNotReportSuccess

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S198.2 batch-intent

Требования: SPEC-0088 R4; один slice — один проверяемый changeset.

Изменение: В существующем spool сохранять bounded source batch intent до fanout: scope/source/probe revision, cursors, exact event IDs/verdicts/deterministic sinks. Replay того же batch восстанавливает remainder. Cursor продвигается только после durable admissions/drop decisions; consuming source без ACK получает ограниченный truthful contract.

Tests (red first; ожидаемый результат определяется SPEC):
- communication tests: TestBatchTenEventsEightSlotsRestartRemainder; TestBatchIntentWriteFailureDoesNotAdvanceCursor; TestPauseDuringBatchPreventsEffect

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S198.3 retry-classification

Требования: SPEC-0088 R4; один slice — один проверяемый changeset.

Изменение: Stable logical operation/attempt IDs. Запрет -r1 после tool/unknown effect; cold wake retry требует доказанного pre-effect state. OAuth refresh retry только после доказанного auth rejection, не любого callErr. Post-effect checkpoint/audit failures сохраняют uncertain. Не обещать provider exactly-once без API evidence.

Tests (red first; ожидаемый результат определяется SPEC):
- runtime/toolhub/communication tests: TestFailedRunAfterToolIsNotResubmitted; TestOAuthTransportTimeoutDoesNotReplayWrite; TestVerifiedPreEffectFailureMayRetry; TestUnknownDeliveryIsNotBlindlyResent

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Не заводить второй transaction engine/queue. Если tool contract меняется, полная catalog/doc/prompt/rebind связка обязательна.

## Acceptance и gates

SPEC-0088 A4.1..A4.3. Сохранить epoch/scope/generation fences и non-consuming source semantics CHG-0214. HTTP/provider fixture доказывает contract, не реальную отправку.

Команды/границы: go test ./internal/communication ./internal/runtime ./internal/toolhub ./internal/routines; root just check; isolated container handoff test.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/communication.md
- docs/routines.md
- docs/services/runtime.md
- docs/operations.md

Изменения agent tools только одним контрактным changeset:
description + internal/stack/prompt_contract.go + docs/tools.md +
docs/routines.md + fresh immutable catalog; acceptance после rebind/respawn.

## Out of scope

Нет новой root DB/queue/framework, изменений upstream Hermes без reviewed pin,
live credential migration или provider delete без выбранного operator plan.
Этот документ не даёт разрешения external send/login/publication.

## Done checklist

- [ ] Все hard dependencies приняты; SQLCipher gate PASS, если нужен.
- [ ] Для новых scenarios сначала red, затем green; existing защиты сохранены.
- [ ] Targeted gates и just check завершены, недоступные честно отмечены.
- [ ] Acceptance evidence разнесён по fixture/container/live; secrets отсутствуют.
- [ ] Current docs перепроверены против кода; state.yaml актуален.
- [ ] Только фактически finished CHG переносится в .work/done/ и индекс обновляется.
