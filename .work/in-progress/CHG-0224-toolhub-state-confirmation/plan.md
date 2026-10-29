# CHG-0224: Сохранность ToolHub и доверенное подтверждение пользователя

Chain: [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) ->
[SPEC-0088 R6](../../../specs/active/SPEC-0088-bounded-control-journals.md) -> этот plan -> state.yaml.

Workstream: Storage reliability MVP (local planning; GitHub records not created).
Дизайн принят 2026-10-28; implementation not started.
Previous: CHG-0223. Next: CHG-0225.
Hard dependencies: CHG-0221, CHG-0222.

## Goal

Сохранность ToolHub и доверенное подтверждение пользователя. Результат этапа проверяется acceptance ниже; текущий работающий код
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

- internal/toolhub/store_io.go: Save, Load, compactLocked, Reload
- internal/toolhub/control.go: statusBody; control_http.go: confirm/status descriptions
- internal/toolhub/connector_journal.go: connectorPhase, applyConfirmedWrites
- internal/communication/spool.go: ApprovalCallback; approval_messages.go: trusted callback wiring; internal/stack/prompt_contract.go
- docs/tools.md; docs/routines.md; internal/agenttools: immutable catalog owner

## Slices

### S200.1 snapshot-durability

Требования: SPEC-0088 R6; один slice — один проверяемый changeset.

Изменение: Directory sync после Save, initialized marker/missing-store refusal, no lost conflict fence. Recreate temp owned volume сохраняет refs и pending operations. Не объявлять missing volume причиной без trace identity evidence.

Tests (red first; ожидаемый результат определяется SPEC):
- toolhub tests: TestSaveDirectorySyncAndConflictFence; TestInitializedMissingStoreRefusesEmptyLoad; TestRecreateRetainsOnboardingAndBrokerReference

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S200.2 onboarding-reasons

Требования: SPEC-0088 R6; один slice — один проверяемый changeset.

Изменение: Различать unknown/scope/expired/terminal/version/storage причины внутренними safe codes. Доказать сохранность workflow на restart и повторном request; не просить credentials заново, если verified ref пригоден. Existing terminal compaction KEEP с correct predicates.

Tests (red first; ожидаемый результат определяется SPEC):
- toolhub tests: TestOnboardingStatusReasonsWithoutSecretLeak; TestRestartResumesKnownConnectionWithoutCredentialEntry

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

### S200.3 human-contract

Требования: SPEC-0088 R6; один slice — один проверяемый changeset.

Изменение: Agent status awaiting_user_confirmation; trusted callback binds human actor/scope/action digest/revision/expiry и atomic single-use consume. Защиты nonce/Origin/CSRF сохраняются. В одном slice descriptions+prompt+tools/routines docs+fresh immutable catalog; затем rebind+respawn при rollout.

Tests (red first; ожидаемый результат определяется SPEC):
- toolhub/communication tests: TestAgentCannotConfirmItsOwnConsent; TestConfirmationWrongActorActionAndExpiryDenied; TestConfirmationConsumedOnceConcurrently

Уровень: unit с реальным temp filesystem и fault injection; где затрагивается
процесс/native storage/контейнер, дополнительно integration по acceptance.

## Red-team checklist

- Wrong actor/context/environment, stale generation и повтор операции не расширяют права.
- Crash/ENOSPC/corrupt input не приводят к потере подтверждённого состояния.
- Unknown path/schema не превращается в разрешение удалить или переинициализировать.
- Cleanup/diagnostic output не содержит secret values, object keys или auth links.
- Не ослабить confirmation ради совместимости модели. Текущие scope-filtered not-found ответы не раскрывать публично.

## Acceptance и gates

SPEC-0088 A6.1/A6.2. Явные authorization negative cases, concurrency и HTTP tests. Protected UI fixture подтверждает contract; реальный account login не входит в автоматическую проверку.

Команды/границы: root just check; HTTP/control integration; catalog consistency tests; future rebind/respawn evidence.
Root gate: just check. Broker: отдельный module pipeline, >=85% total/per
production package; root own Go >=85%. Нет live claim по mock.
На targeted Docker rebuild использовать hubctl dev-rebuild или just, сверить
actual image и endpoint. Недоступный gate записать как unavailable с причиной.

## Current docs после реализации

- docs/services/toolhub.md
- docs/tools.md
- docs/routines.md
- docs/prepared-connectors.md

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
