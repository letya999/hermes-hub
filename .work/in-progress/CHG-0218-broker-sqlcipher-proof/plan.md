# CHG-0218: Gate сборки и аварийной атомарности SQLCipher

Chain: [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) ->
[SPEC-0087 R2](../../../specs/active/SPEC-0087-broker-bounded-storage.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0217. Next: CHG-0219.
Hard dependencies: CHG-0215.

## Goal

Gate сборки и аварийной атомарности SQLCipher. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- services/credential-broker/go.mod; go.sum; deploy/Dockerfile; scripts/quality.py
- docker/Dockerfile: только credential-broker build stage
- services/credential-broker/internal/journal/recovery_test.go: baseline failure semantics
- proposed internal/storeproof integration harness в модуле Broker, не runtime backend

## Slices

### S194.1 engine-build

Требования: SPEC-0087 R2; один slice — один проверяемый changeset.

Изменение: Выбрать поддерживаемый Go binding к актуальному stable SQLCipher Community; зафиксировать exact engine/binding/crypto hashes, patched SQLite version и licenses. Воспроизводимая Linux broker image; root Go binaries без новых native deps. Проверить actual cipher_version/sqlite_version, не имя пакета.

Tests (red first; ожидаемый результат определяется SPEC):
- proposed storeproof: TestSQLCipherBuildIdentity; TestBrokerOnlyNativeDependency; TestSQLCipherNoPlaintextTemporaryArtifacts

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S194.2 atomicity-matrix

Требования: SPEC-0087 R2; один slice — один проверяемый changeset.

Изменение: Одна ATTACH transaction P/R/W, recovery=main, DELETE+EXTRA, explicit independent keys/temp_store. Детерминированные fault points + process kill. Тесты запускаются только на synthetic data и disposable volumes.

Tests (red first; ожидаемый результат определяется SPEC):
- proposed storeproof: TestAttachedCommitCrashMatrix; TestPrimaryVolumeLossAtCommitPoints; TestWrongKeyAndStaleWitnessRejected; TestDiskFullKeepsAcknowledgedRevoke

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S194.3 gate-receipt

Требования: SPEC-0087 R2; один slice — один проверяемый changeset.

Изменение: Записать evidence engine-pins.md + gate-result.md: version/hash/config, fault point, last acknowledged head, restored head, memory/latency conditions. Outcome pass/fail/unavailable. Успех повторяется на целевом filesystem; mock VFS не заменяет container proof.

Tests (red first; ожидаемый результат определяется SPEC):
- proposed storeproof: TestLostCommitResponseUsesSameOperation; TestHotJournalRecoveryWithoutManualDeletion

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- ATTACH atomicity из документации не является доказательством выбранной volume-loss модели. Не выпускать prototype в production и не создавать fallback mirrored-WAL protocol.

## Acceptance и gates

SPEC-0087 A2.1/A2.2. PASS только при сохранении всех R2 invariants, reproducible native build и actual volume failure matrix. FAIL/UNAVAILABLE блокируют CHG-0219/0196, не независимые root-plane repairs.

Команды/границы: Module test-integration/test-race/check + security/dependency audit; real Linux broker image gate. Native unavailable честно фиксировать.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/credential-broker/engineering/testing.md
- gate-result.md в данном CHG; описание actual current docs только при shipping changes

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
