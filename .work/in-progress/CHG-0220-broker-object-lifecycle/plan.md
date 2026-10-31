# CHG-0220: Управляемые версии secret objects и безопасный GC

Chain: [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) ->
[SPEC-0087 R4](../../../specs/active/SPEC-0087-broker-bounded-storage.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0219. Next: CHG-0221.
Hard dependencies: CHG-0219.

## Goal

Управляемые версии secret objects и безопасный GC. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- services/credential-broker/provider/local.go: Write, Delete, BackupExisting
- services/credential-broker/broker/oauth.go: StartOAuth, CompleteOAuth, AccessToken
- services/credential-broker/broker/runtime.go: Release checkpoint
- services/credential-broker/broker/enrollment.go: Submit, ready; grants.go: DeleteCredential; broker.go: RecoveryRefs
- new object rows/cleanup queue из CHG-0219; internal/credentialbroker/resolve.go

## Slices

### S196.1 version-ownership

Требования: SPEC-0087 R4; один slice — один проверяемый changeset.

Изменение: Заменить растущие OwnedRefs на current/pinned/retired rows и ownership operation. До local object creation persist intent с точным locator/op ID; metadata не содержат values. У remote provider без reservable ref оставить explicit unknown-orphan limitation.

Tests (red first; ожидаемый результат определяется SPEC):
- provider/broker tests: TestLocalCreateCrashBeforeActivationOwned; TestRefreshMetadataGrowthIsLinear; TestUnknownRemoteObjectNeverDeleted

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S196.2 safe-retirement

Требования: SPEC-0087 R4; один slice — один проверяемый changeset.

Изменение: Pins active use и required quiesced restore; activate replacement до retirement. Сравнить checkpoint content безопасным broker-side digest: unchanged state не создаёт новую version при действительном совпадении identity/content. OAuth rotating tokens не дедуплицируются по предположению.

Tests (red first; ожидаемый результат определяется SPEC):
- broker tests: TestPinnedCheckpointVersionSurvivesGC; TestUnchangedQuiescedCheckpointDoesNotCreateVersion; TestRotatingOAuthReplacementNotLost

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S196.3 bounded-cleanup

Требования: SPEC-0087 R4; один slice — один проверяемый changeset.

Изменение: Bounded idempotent pending-delete worker, backoff, exact known refs, cleanup primary+recovery copies + sync. Expired drafts/verifiers освобождаются только по ownership. Не запускать lease/materialization для metadata inventory; plaintext cache не вводить.

Tests (red first; ожидаемый результат определяется SPEC):
- provider/recovery tests: TestDeleteFailureRemainsPendingAndDenied; TestCleanupBothCopiesBeforeReceipt; TestAliasAndSharedCredentialUntouched

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Старые backup copies могут сохранять deleted secret: deletion policy должна охватывать backups; гарантии physical erasure SSD не заявлять.

## Acceptance и gates

SPEC-0087 A4.1/A4.2. 1000 synthetic refresh/checkpoint циклов, active pinned version, abandoned own intent и unknown object; проверить retained set, live restore и absence of raw materials в отчёте.

Команды/границы: Module provider contract/integration/race/check; root credentialbroker tests + just check.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/credential-broker/architecture/providers-and-state.md
- docs/services/credential-broker/architecture/oauth.md
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
