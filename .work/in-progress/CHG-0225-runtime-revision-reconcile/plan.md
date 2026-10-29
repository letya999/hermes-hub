# CHG-0225: Долговечная сверка проекций и понятные причины runtime-сбоев

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R7](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0224. Next: CHG-0226.
Hard dependencies: CHG-0217, CHG-0224.

## Goal

Долговечная сверка проекций и понятные причины runtime-сбоев. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/toolhub/api/restart_notify.go: RuntimeRestartNotifier.Notify
- internal/toolhub/api projection revisions; internal/toolhub/reconciler.go
- internal/supervisor/control.go; ensure.go; incidents.go; runtime ownership state
- internal/runtime/model_relay.go: modelHop, modelCall, waitForModelRecovery; server_observe.go
- internal/communication: title generation and terminal result paths

## Slices

### S201.1 desired-applied

Требования: SPEC-0088 R7; один slice — один проверяемый changeset.

Изменение: Persist desired/applied revision, semantic tools/permission digest, request operation ID and reason. На startup reconcile outstanding, не RAM priming. Hash не меняется от incidental health metadata. Idempotent restart requests bind target generation/revision.

Tests (red first; ожидаемый результат определяется SPEC):
- toolhub/api tests: TestRestartNotifierRecoversUnappliedRevision; TestSemanticSurfaceUnchangedDoesNotRestart; TestProjectionBurstAppliesLatestOnce

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S201.2 restart-causes

Требования: SPEC-0088 R7; один slice — один проверяемый changeset.

Изменение: Supervisor distinguishes planned rebuild/idle/tool-change/fault with owner-safe IDs; coalesce soft restarts после handoff. Revoke enforced immediately by ToolHub even while compute running. Не чинить epoch защиту принудительной заменой всех epochs.

Tests (red first; ожидаемый результат определяется SPEC):
- supervisor/toolhub tests: TestRevocationDoesNotWaitForSoftRestart; TestRestartReasonSurvivesSupervisorRestart; TestStatusDeliveryDoesNotTriggerFinalRestart

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S201.3 failure-semantics

Требования: SPEC-0088 R7; один slice — один проверяемый changeset.

Изменение: Reuse configured model fallback/cooldown, typed unavailable, preserve partial result. Title generation best-effort отдельный исход. На pinned Hermes проверить tool IsError vs transport; если требуется upstream change — reviewed pin/adapter отдельным traceable slice без vendored patch.

Tests (red first; ожидаемый результат определяется SPEC):
- runtime/communication tests: TestModelOutageHasTypedCause; TestTitleFailureKeepsCompletedResult; TestPinnedToolErrorNeverReportsFalseSuccess

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- SIGTERM сам по себе не авария; нужны причины. Не добавлять новый routing provider или guessed API.

## Acceptance и gates

SPEC-0088 A7.1/A7.2. Isolated real stack: stop notifier between desired/applied, resume, сравнить actual catalog/image/revision; permissions remain denied during restart.

Команды/границы: root just check; supervisor/runtime/ToolHub container gate; pinned-Hermes contract probe.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/supervisor.md
- docs/services/model-relay.md
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
