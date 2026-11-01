# CHG-0219: Таблицы состояния и постраничный audit Broker

Chain: [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) ->
[SPEC-0087 R3](../../../specs/active/SPEC-0087-broker-bounded-storage.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0218. Next: CHG-0220.
Hard dependencies: CHG-0215, CHG-0218.

## Goal

Таблицы состояния и постраничный audit Broker. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- services/credential-broker/broker/broker.go: New, apply, commit, Events
- services/credential-broker/broker/types.go: Config, mutation и records
- services/credential-broker/app/config.go; internal/journal; proposed internal/store SQL migrations
- services/credential-broker/api/v1/types.go; api/openapi.json; httpapi/server.go; client/client.go; scripts/openapi.py

## Slices

### S195.1 transactional-state

Требования: SPEC-0087 R3; один slice — один проверяемый changeset.

Изменение: После PASS 0194 создать нормализованный state/audit/object tables и schema_version. Один SQL writer. Mutation+safe audit+recovery witness по проверенному протоколу. Удалить зависимость Broker.New от полной Records()/b.events после активации нового формата. SQL traces и DSN не содержат keys.

Tests (red first; ожидаемый результат определяется SPEC):
- broker/storage tests: TestStateAuditCommitTogether; TestRestartPreservesRevokeAndDropsLeases; TestNoSecretMaterialInStateAuditRows

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S195.2 events-retention

Требования: SPEC-0087 R3; один slice — один проверяемый changeset.

Изменение: Indexed scoped pagination <=100 с прежним global sequence. Явные earliest_available/cursor_expired/gap без silent reset; обновить HTTP/OpenAPI/client в одном slice, negative scope tests. Hot age/bytes -> encrypted archive+sealed manifest до prune, destructive removal только по explicit policy.

Tests (red first; ожидаемый результат определяется SPEC):
- broker/httpapi/client tests: TestScopedEventsPageAndExpiredCursor; TestForeignCursorNeverReturnsOtherScope; TestAuditArchiveVerifiedBeforePrune

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S195.3 import-and-scale

Требования: SPEC-0087 R3; один slice — один проверяемый changeset.

Изменение: Synthetic legacy importer read-only: preserve IDs, contract digest, state, refs, tombstones и sequences. 10k/100k/1M audits с одинаковым current state/cache; record actual memory/latency. Полная history integrity проверяется streaming. Production switch только в CHG-0226.

Tests (red first; ожидаемый результат определяется SPEC):
- module integration: TestLegacyImportPreservesIdentityAndHead; TestAuditScaleUsesBoundedWorkingSet; TestImportFailureLeavesLegacySourceUntouched

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Не добавлять secrets в SQL только потому, что encrypted. Не менять pinned SPEC-0064 hash или молча отбрасывать старый audit.

## Acceptance и gates

SPEC-0087 A3.1..A3.3 и A5.1 в части store. Повреждённые rows/witness и stale valid authorization image не дают доступ. Data migration/live activation ещё не выполнены.

Команды/границы: Module check >=85% total/per package + race/fuzz; root client/stack integration + just check; dependency security.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/credential-broker/architecture/ledger.md
- docs/services/credential-broker/architecture/api-contract.md
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
