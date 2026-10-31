# CHG-0217: Ротация, курсоры и ограниченный trace диагностики

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R2](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0216. Next: CHG-0218.
Hard dependencies: нет.

## Goal

Ротация, курсоры и ограниченный trace диагностики. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/diagnostics/collector.go: Collect, Follow, cursor, DockerRun
- internal/diagnostics/host.go: HostLog, boundedFile.Write
- internal/diagnostics/trace.go: Trace, traceJobIdentity, traceMatches, traceLineTime
- internal/diagnostics/log.go; cmd/hubctl/trace.go: runTrace, traceDockerNames, traceDockerLogs; internal/toolhub/diagnostics.go

## Slices

### S193.1 rotation

Требования: SPEC-0088 R2; один slice — один проверяемый changeset.

Изменение: Заменить O_TRUNC конечной ротацией; max line/batch/file/count/age. Host cursor содержит generation/identity файла. Использовать существующий safe scope resolver; не собирать весь host.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/diagnostics: TestRotateKeepsBoundedHistory; TestHostRotationResumesByGeneration

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S193.2 source-cursors

Требования: SPEC-0088 R2; один slice — один проверяемый changeset.

Изменение: Docker read — bounded streaming от oldest unread, без --tail последних 5000. Parsed RFC3339Nano + tie ordinal/source generation, data sync до cursor, atomic cursor replace. Dead read failure и source retention loss дают gap; controlled batch stop не считается source error.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/diagnostics: TestCollect5001LinesWithoutSkip; TestCollectEqualTimestampsAcrossCycles; TestCollectCrashBeforeCursorReplaysSafely; TestStoppedUnreadableContainerReportsGap

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S193.3 trace

Требования: SPEC-0088 R2; один slice — один проверяемый changeset.

Изменение: Correct env/home path, exact IDs и authority-derived owner; cap output/deadline; dedup source identity; explicit partial result/gaps. Producer allowlist прежде Redact; содержимое отключено по умолчанию.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/diagnostics: TestTraceManagedEnvironmentLogs; TestTraceUnknownOwnerCannotScanForeignContainers; TestTraceExactIDAndOutputLimit

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Истёкшие исходные Docker logs невосстановимы: показывать gap, не обещать lossless security audit из diagnostics.

## Acceptance и gates

SPEC-0088 A2.1/A2.2. Controlled Docker container испускает synthetic lines без secrets; отдельно проверить actual rotation Docker Desktop/Linux, fixtures не считать доказательством daemon behaviour.

Команды/границы: go test ./internal/diagnostics ./cmd/hubctl; root just check; container smoke для Docker stream.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/hubctl.md
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
