# CHG-0226: Явная миграция и итоговая приёмка ограниченного хранения

Chain: [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) ->
[SPEC-0087 R6](../../../specs/active/SPEC-0087-broker-bounded-storage.md) -> этот plan -> state.yaml.
Также: SPEC-0088; конкретные cross-spec acceptance указаны ниже.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0225. Next: приёмка завершает цепочку.
Hard dependencies: CHG-0215, CHG-0216, CHG-0217, CHG-0218, CHG-0219, CHG-0220, CHG-0221, CHG-0222, CHG-0223, CHG-0224, CHG-0225.

## Goal

Явная миграция и итоговая приёмка ограниченного хранения. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- services/credential-broker/app/recovery.go и importer/store activation CLI
- internal/communication layout migration/recovery; cmd/hubctl storage and doctor
- internal/stack/broker_recovery_test.go; internal/e2e/registry.json
- docs/operations.md; docs/services/credential-broker/operations/recovery.md; affected service docs

## Slices

### S202.1 read-only-import

Требования: SPEC-0087 R6; один slice — один проверяемый changeset.

Изменение: Fenced writers, verified encrypted backup, source read-only scan, manifest counts/IDs/scopes/sequence/refs/head; stage new DB/layout. Crash points before/after stage/verify/activation resume same migration. Config/TLS/contracts/keys separately backed up.

Tests (red first; ожидаемый результат определяется SPEC):
- module/app integration: TestMigrationCrashResumePreservesSource; TestMigrationPreservesRevokeAndScope; TestInitializedMissingStoreNeverReinitializes

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S202.2 dev-acceptance

Требования: SPEC-0087 R6; один slice — один проверяемый changeset.

Изменение: On disposable volumes run complete A matrix. Только затем explicit selected dev migration с receipts, actual image/catalog version, rebind/respawn where needed. No login/external send/provider destructive mutation unless actual instruction; blocked live claims remain unproven.

Tests (red first; ожидаемый результат определяется SPEC):
- root integration: TestStoragePressureRestartAndDeliveryRecovery; TestPrimaryVolumeRestoreWithIndependentConfigAndKeys; TestOldBinaryCannotOpenNewFormat

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S202.3 closeout

Требования: SPEC-0087 R6; один slice — один проверяемый changeset.

Изменение: Record measured 10k/100k/1M resource comparison, bounded file growth, pending cleanup and incident observability. Update current docs truthfully, run full gates, move finished CHGs to done same day. Roll-forward only after new revokes; no old-backup rollback. No source deletion until verified plan/policy.

Tests (red first; ожидаемый результат определяется SPEC):
- acceptance.md из CHG-0215: all scenario IDs, evidence level and exact observed result

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Несколько local volumes не дают full-host HA. Не откатывать grant state после новых revokes. Не называть fixture или interrupted gate integration success.

## Acceptance и gates

SPEC-0087 A6.1/A6.2 и SPEC-0088 A8.1; все предыдущие A rows. Acceptance receipts содержат safe aggregates и assertions, не personal transcripts/credential object keys.

Команды/границы: root just check; module check/test-integration/test-e2e/test-race/fuzz/release-check по доступности; just security при deps; Docker image+contract gates.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/architecture.md
- docs/operations.md
- docs/services/credential-broker/architecture/ledger.md
- docs/services/communication.md
- docs/services/toolhub.md
- docs/validation.md

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
