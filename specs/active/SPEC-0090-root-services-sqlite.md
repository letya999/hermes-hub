---
description: "Замороженные требования к SQLite-хранению ToolHub и Communication: схемы, транзакции, лимиты, миграция и приёмка."
last_verified: "2026-10-31"
---

# SPEC-0090: Root services SQLite storage MVP

Frozen: 2026-10-31. Design accepted; implementation not started.
Решение: [ADR-0091](../../docs/adr/ADR-0091-root-services-sqlite-storage.md).
План: [CHG-0215..0226](../../.work/in-progress/CHG-0215-broker-recovery-head/README.md).
Дополняет SPEC-0087/0088: пункт «Никакого перехода ToolHub/Communication/
Hermes на новую БД» в SPEC-0088 отменяется для ToolHub и Communication;
Hermes memory/artifacts/usage сохраняют прежних владельцев.

## R1. Общий substrate (все три сервиса)

- R1.1: Одна embedded БД на сервис на space; без shared DB, ORM, external
  server и CGO в root binaries. Single writer; существующий process fence
  сохраняется поверх движковых блокировок.
- R1.2: Mutation, audit/receipt и связанные производные записи фиксируются
  одной транзакцией. Ack — после durability движка; ни один подтверждённый
  переход не теряется при kill/ENOSPC.
- R1.3: Схема versioned; открытие незнакомой версии — refusal, не repair
  и не silent bootstrap. Initialized store, потерявший marker/DB, не
  превращается в новый пустой store.
- R1.4: Secret values в таблицы не входят. Sensitive payloads остаются в
  существующих encrypted stores/sealed fields; DB файл 0600 в per-space
  volume. Dev/prod не разделяют и не копируют DB.

## R2. ToolHub state (заменяет substrate SPEC-0088 R6)

- R2.1: Все persisted коллекции (definitions, connections, credentials
  refs, bindings, workloads, grants, onboardings, operations, publications,
  policies, teams, broker grants, capability records, recipes, exclusions,
  projections, restart revisions/digests, changes) пишутся одной
  транзакцией с installation/schema/digest/generation метаданными.
- R2.2: Conflict fence сохранён: write при чужом digest — отказ в той же
  транзакции. Reload детектирует чужое поколение без полного decode.
  Initialized-missing/corrupt — refusal; пустой успешный Store не создаётся.
- R2.3: Service audit ledger остаётся finite segments по SPEC-0088 R3
  (принятый substrate). Транзакционная связь audit↔state требуется только
  у Broker (SPEC-0087 R3.1).

## R3. Communication durable records (заменяет substrate SPEC-0088 R1–R4)

- R3.1: Jobs (pending/running/done/failed), mappings + idempotency index,
  outbox deliveries, run receipts, event-state cursors, provider dedup,
  callbacks, conversations, tasks, budget ledger, usage journal, inbound
  мета и routines durable records (schedules, occurrences, watchers,
  digests, assignments, hooks, goals) — строки records-таблиц по
  (dir,name); переходы состояний — UPDATE в транзакции.
- R3.2: Binary payloads, tmp/, архивы и mock-* остаются bounded файлами;
  storage accounting включает и DB, и файловый остаток.
- R3.3: Recovery из recover() сохраняет семантику: running без terminal
  mapping → uncertain, никогда silent retry; маппинги и idempotency
  перестраиваются из таблиц, а не из directory scans.
- R3.4: Cursor/dedup/receipt гарантии SPEC-0088 R1/R3/R4 сохраняются на
  новом substrate: активный cursor не terminal history, batch intent до
  fanout, canonical receipt до производных записей.

## R4. Broker SQLCipher store (без изменений из SPEC-0087)

Требования SPEC-0087 R1–R6 действуют; SPEC-0090 лишь сводит общий контракт
миграции и приёмки трёх сервисов (R5/R6 ниже). CHG-0218 native gate
остаётся обязательным до CHG-0219.

## R5. Миграция и активация (единый контракт)

- R5.1: Fence writers → verified read-only backup → import без изменения
  source → сверка IDs/counts/scopes/events → versioned manifest → atomic
  activation → health acceptance. Источник read-only для импортёра;
  mutating open как верификатор запрещён.
- R5.2: Activation marker отвергается старым бинарником; старый writer
  не создаёт параллельный legacy store. Crash на любом шаге продолжает
  ту же операцию по manifest.
- R5.3: Удаление legacy layout — только после successful dev acceptance
  и operator `--apply` со scoped plan digest; unknown paths report-only.
  Новые mutation/revokes после backup запрещают rollback старой копии.
- R5.4: Креды, grants/revokes, Telegram-сессии каналов и provider objects
  сохраняются; secret material не появляется в манифестах, логах,
  diagnostics и новых таблицах.

## R6. Приёмка

- R6.1: Каждый сервис стартует на новой БД, принимает и подтверждает
  мутации, переживает restart/kill без потери ack.
- R6.2: Fixture/container/live evidence раздельно; root just check,
  broker module pipeline (>=85% overall и per-package), race/fuzz,
  security при смене зависимостей, Docker build через hubctl/just.
- R6.3: Dev live E2E: message→job→delivery roundtrip, tool admission с
  audit, broker grant/revoke + restart, migration dry-run/apply на
  реальном space. Не объявлять переход завершённым до трёх зелёных.

## Acceptance

| ID | Сценарий и обязательное наблюдение |
|---|---|
| A1.1 | Kill до/после commit на каждом сервисе: подтверждённое состояние целостно, uncommitted tail не фантомится |
| A1.2 | Missing/corrupt DB или marker на initialized store: refusal без перезаписи исходных bytes |
| A2.1 | ToolHub: concurrent Save + migrate + restart — digest fence держится, grants/connections идентичны source |
| A3.1 | Communication: burst jobs+deliveries, crash mid-recover — нет silent retry, mapping/idempotency согласованы с receipts |
| A4.1 | Broker: SPEC-0087 fault matrix на реальном SQLCipher; revoke переживает потерю primary |
| A5.1 | Полный footprint (DB + sidecars + payloads + scratch) в пределах budget; 80% — bounded maintenance |
| A6.1 | Migration crash matrix + activation + dev E2E на каждом сервисе; старый writer отвергнут |
| A6.2 | Рост логов/audit ограничен: pages бounded, нет O(history) RAM на hot path |
