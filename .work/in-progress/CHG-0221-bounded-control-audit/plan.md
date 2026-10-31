# CHG-0221: Ограниченный обязательный audit и честные исходы действий

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R3](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0220. Next: CHG-0222.
Hard dependencies: нет.

## Goal

Ограниченный обязательный audit и честные исходы действий. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/audit/audit.go: Ledger.Open, Append, List
- internal/toolhub/gateway_call.go: call, audit; server.go: AuditWrite
- internal/sshcap/service.go: auditEvent и callers; internal/exposure/service.go: audit и callers
- internal/hosting/audit.go: reference pattern, не import dependency

## Slices

### S197.1 bounded-ledger

Требования: SPEC-0088 R3; один slice — один проверяемый changeset.

Изменение: Versioned finite JSONL segments; file+directory sync, existing flock для append+rotation между процессами; single logical record limit, scoped paged/tail read. Corrupt/partial tail не обрезается молча. Default existing audit не получает silent destructive TTL.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/audit tests: TestConcurrentAppendRotationAcrossWriters; TestAuditBoundedReadAndScope; TestAuditCorruptTailReportedWithoutMutation

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S197.2 admission-outcome

Требования: SPEC-0088 R3; один slice — один проверяемый changeset.

Изменение: Separate auth decision/execution/delivery fields с correlation IDs; preserve privacy. Durable admitted operation и completion reserve до эффекта, unsettled recovery после crash. Проверить required audit errors на всех callers SSH/exposure/toolhub. Post-effect failure -> uncertain, не retryable transport failure.

Tests (red first; ожидаемый результат определяется SPEC):
- toolhub/sshcap/exposure tests: TestAuditFailureBeforeEffectPreventsCall; TestPostEffectAuditFailureNeverRepeatsAction; TestBackendErrorIsNotAuthorizationDeny

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S197.3 policy-and-recovery

Требования: SPEC-0088 R3; один slice — один проверяемый changeset.

Изменение: Known unfinished audit intent переживает rotation/GC и получает visible interrupted/unknown, не fake success. Revocation/cleanup используют reserved lifecycle space. SafeReceipt и producer allowlist, не generic map dump.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/audit tests: TestCompletionReserveSurvivesHighWater; TestUnsettledAdmissionSurvivesRetention; TestAuditFieldsRejectSensitivePayload

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Audit failure после реального эффекта нельзя исправить повтором backend. Не ослаблять возможность немедленного прекращения доступа.

## Acceptance и gates

SPEC-0088 A3.1/A3.2. Real temp FS plus concurrency/process boundary. Existing Hosting audit tests KEEP как reference; новые audit tests проверяют общий writer и настоящие consumers.

Команды/границы: go test ./internal/audit ./internal/toolhub ./internal/sshcap ./internal/exposure; root just check.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/toolhub.md
- docs/threat-model.md
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
