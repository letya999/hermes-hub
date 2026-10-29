# CHG-0216: Изоляция курсоров и безопасная очистка event records

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R1](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0215. Next: CHG-0217.
Hard dependencies: нет.

## Goal

Изоляция курсоров и безопасная очистка event records. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/communication/events.go: eventPath, loadEventStateLocked
- internal/communication/stream.go: RecordStreamEvent, recoverStreamEvents
- internal/communication/slack.go: provider dedup path
- internal/communication/spool.go: recover, Compact, compactDirLocked, appendArchiveLocked

## Slices

### S192.1 retention-regressions

Требования: SPEC-0088 R1; один slice — один проверяемый changeset.

Изменение: Исправить predicate текущего events/ до миграции: cursor активного/paused/snoozed source не является terminal receipt. Непонятный schema не считается разрешением удалить файл.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/communication/spool_compact_test.go: TestCompactKeepsPausedSourceCursorAtAgeAndFileLimit; TestCompactDoesNotDeleteUnknownEventSchema

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S192.2 layout-migration

Требования: SPEC-0088 R1; один slice — один проверяемый changeset.

Изменение: Развести event-state/, run-events/, provider-dedup/. Одноразовый strict migration под существующим writer fence; format marker/manifest, directory sync, повтор после crash. Dual read допустим только в миграторе; обычный writer работает с одним активным layout.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/communication/recovery_test.go: TestEventLayoutMigrationCrashResume; TestMissingInitializedCursorShowsGap; TestEventLayoutForeignScopeRejected

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S192.3 durable-archive

Требования: SPEC-0088 R1; один slice — один проверяемый changeset.

Изменение: Перед удалением original sync и verify archive member, sync directory. При partial gzip member сохранить source и возобновить по stable record ID; count/TTL GC проверяет ownership и live references каждого типа.

Tests (red first; ожидаемый результат определяется SPEC):
- internal/communication/spool_compact_test.go: TestArchiveSyncFailureKeepsSource; TestArchiveReplayDoesNotLoseOrDuplicateRecord

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Не reset cursor при отсутствии/повреждении файла. Не менять grant/assignment semantics CHG-0214.

## Acceptance и gates

SPEC-0088 A1.1..A1.3. Реальный temp filesystem: cursor старше retention, pending delivery и неизвестный файл; Compact не затрагивает их, terminal source после verified archive можно убрать.

Команды/границы: go test ./internal/communication ./internal/routines; root just check.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/communication.md
- docs/routines.md
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
