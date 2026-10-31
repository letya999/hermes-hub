# CHG-0223: Полный учёт объёма и безопасное операторское обслуживание

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R5](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.
Также: SPEC-0087; конкретные cross-spec acceptance указаны ниже.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0222. Next: CHG-0224.
Hard dependencies: CHG-0217, CHG-0219, CHG-0220, CHG-0221, CHG-0222.

## Goal

Полный учёт объёма и безопасное операторское обслуживание. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/communication/budget.go: checkCeilingsLocked; control.go: Compact loop; spool.go: Compact
- internal/toolhub/store_io.go: compactLocked; internal/diagnostics; internal/audit
- services/credential-broker/app/config.go и store/provider maintenance owners
- internal/stack/doctor.go; cmd/hubctl: doctor/storage status/cleanup CLI wiring

## Slices

### S199.1 all-owned-bytes

Требования: SPEC-0088 R5; один slice — один проверяемый changeset.

Изменение: Accounting всех owned dirs и full P/R/archive/scratch footprint. Bounded counters с periodic reconciliation и file-count cap; учитывать next maximum write/lifecycle reserve. Pending-only ceiling исправить. У metadata hot-path не запускать полный recursive walk на каждый запрос.

Tests (red first; ожидаемый результат определяется SPEC):
- communication/broker tests: TestStorageCeilingCountsOutboxArchivesAndRecovery; TestMaximumNextWriteUsesReserve; TestAccountingReconcilesAfterCrash

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S199.2 owner-maintenance

Требования: SPEC-0088 R5; один slice — один проверяемый changeset.

Изменение: 80% high water, per-owner bounded GC/rotation/export с backoff. Сохранить existing ToolHub 30d removed/7d terminal и configurable spool windows; учитывать protected cursors/receipts/refs. Ошибка Compact не игнорируется, сохраняется last error/last success.

Tests (red first; ожидаемый результат определяется SPEC):
- communication/toolhub tests: TestCleanupFailureVisibleAndBackedOff; TestMaintenanceNeverPrunesLiveState; TestAuditNoPolicyExportsWithoutDeleting

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S199.3 safe-operator-cli

Требования: SPEC-0088 R5; один slice — один проверяемый changeset.

Изменение: Расширить doctor safe aggregates; добавить storage status и cleanup --dry-run/--apply. Plan digest bind scope/env/generation/policy; apply rechecks writers/paths/symlinks. Object names, auth data и unknown paths не выводятся/не удаляются. Limits/defaults конечны и проверяются config validators.

Tests (red first; ожидаемый результат определяется SPEC):
- cmd/hubctl tests: TestStorageStatusAllowlistedFieldsOnly; TestCleanupStalePlanAndForeignScopeRejected; TestCleanupSymlinkEscapeDenied

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Finite storage + no destructive audit policy => admission stop, а не обещание бесконечного архива. Не применять cleanup к живому deployment в рамках проверки без выбранного plan/policy.

## Acceptance и gates

SPEC-0088 A5.1/A5.2 и SPEC-0087 A5.1. Small synthetic quotas, реальные allocated bytes/free space где доступны; status отдельно показывает logical size и files, не выдаёт их за universal physical accounting.

Команды/границы: root just check; module config/maintenance tests и module check; CLI safe fixture integration.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/hubctl.md
- docs/operations.md
- docs/services/credential-broker/operations/recovery.md

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
