# CHG-0215: Надёжная текущая голова recovery брокера

Chain: [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) ->
[SPEC-0087 R1](../../../specs/active/SPEC-0087-broker-bounded-storage.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: нет. Next: CHG-0216.
Hard dependencies: нет.

## Goal

Надёжная текущая голова recovery брокера. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- services/credential-broker/internal/journal/recovery.go: recoveryAnchor.latest, commit, openReplicated, SeedReplicatedFromOpen, truncateTo
- services/credential-broker/internal/journal/journal.go: Open, Append
- services/credential-broker/app/recovery.go; app/config.go: initialization и migration wiring

## Slices

### S191.1 recovery-regressions

Требования: SPEC-0087 R1; один slice — один проверяемый changeset.

Изменение: На synthetic grant(seq1)/revoke(seq2) воспроизвести удаление только latest anchor, всех commit-файлов при сохранном initialized, неправильный key и genesis/interrupted first write. До проверки отсутствия доверенного head ничего не truncate/compact.

Tests (red first; ожидаемый результат определяется SPEC):
- services/credential-broker/internal/journal/recovery_test.go: TestMissingLatestAnchorNeverReactivatesGrant; TestMissingInitializedHeadPreservesJournalBytes

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S191.2 versioned-head

Требования: SPEC-0087 R1; один slice — один проверяемый changeset.

Изменение: Добавить sealed current-head v2 с explicit genesis, purpose/store binding и atomic replace+file/dir sync. Ack остаётся после backup+primary+head. Missing/corrupt current head всегда refusal; предыдущая копия не используется как fallback. В новой версии initialization marker неприемлем для старого binary.

Tests (red first; ожидаемый результат определяется SPEC):
- services/credential-broker/internal/journal/recovery_test.go: TestCurrentHeadAckCrashBoundaries; TestCurrentHeadMissingDoesNotFallBack; TestExplicitGenesisAllowsInterruptedFirstWrite

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S191.3 legacy-adoption

Требования: SPEC-0087 R1; один slice — один проверяемый changeset.

Изменение: Offline migrate-head на согласованной копии: source scan read-only, P/R/legacy latest equal, same hashes. Верифицировать v2 перед activation. Несогласованный набор — recovery-required без изменения source. Старые commit-файлы архивируются/удаляются только scoped операторским plan после перехода.

Tests (red first; ожидаемый результат определяется SPEC):
- services/credential-broker/app/recovery_test.go: TestHeadMigrationRequiresQuiescedMatchingCopies; TestHeadMigrationResumeAndOldBinaryRefusal

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Legacy file deletion и неподтверждённый tail неразличимы без нового current-head: не делать автоматическое adoption на живом mismatched наборе.

## Acceptance и gates

SPEC-0087 A1.1/A1.2. На изолированном tmp store подтвердить прежний revoke и byte-for-byte неизменность source при refusal; существующие tail-discard тесты сохранить для доказанно uncommitted tail.

Команды/границы: Broker test-unit/test-integration/test-race + module check; root just check для app/Hub wiring.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/credential-broker/architecture/ledger.md
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
