---
description: "Замороженные требования к ограниченному хранению Broker, recovery, provider versions и явной миграции."
last_verified: "2026-10-28"
---

# SPEC-0087: Broker bounded storage MVP

Frozen: 2026-10-28. Design accepted; implementation not started.
Решение: [ADR-0087](../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md).
План: [CHG-0215..0202](../../.work/in-progress/CHG-0215-broker-recovery-head/README.md).
Дополняет SPEC-0064 R5/R7/R9/R10/R12/R13/R15 и SPEC-0068; не меняет
hash-pinned baseline. Требования применяются к новому формату после миграции.

## R1. Подтверждённое состояние (CHG-0215)

- R1.1: Один versioned sealed current-head содержит store identity, format,
  sequence и digest. Даже genesis имеет явный head. Missing/corrupt head
  initialized store блокирует startup без изменения исходных файлов.
- R1.2: Ack только после recovery, primary и head durability. Недостоверность
  head не разрешает выбирать старший из оставшихся commit-файлов или молча
  truncating rollback. Доказанный uncommitted tail обрабатывается отдельно.
- R1.3: Legacy adoption только offline: primary/recovery/anchor согласованы,
  исходник скопирован, новый marker не принимается старым бинарником.
  Old anchors удаляются лишь после verified switch и по scoped plan.
- R1.4: Режимы bootstrap, migration, normal recovery различимы. Утрата файла
  initialization не превращает существующую установку в новую.

## R2. SQLCipher gate (CHG-0218)

- R2.1: Зафиксировать поддерживаемые native SQLCipher/SQLite/crypto/binding
  версии и hashes, license/build provenance и embedded patched SQLite version.
  Воспроизводимы broker Go tests и production Linux image; root binaries
  не получают CGO dependency. Случайный старый bundled SQLite запрещён.
- R2.2: Проверить independently keyed primary/recovery/witness ATTACH commit:
  recovery=main, DELETE, EXTRA, bounded plaintext-free temp storage, one writer.
  PRAGMAs проверяются фактически, ошибка настройки не превращается в warning.
- R2.3: Kill points до/после каждого durable шага, primary absent mid-commit,
  ENOSPC, wrong key, hot journal и stale copy дают согласованное состояние
  либо отказ. Hot journals не удаляются вручную. После acknowledged revoke
  active grant не восстанавливается.
- R2.4: Lost HTTP response после commit разрешается lookup тем же operation ID.
  Gate result содержит exact build, fixture, failure point, observed head и
  recovery result без secrets. Непрошедший gate блокирует CHG-0219/0196.
  Прототип не включается в production и не создаёт второй shipping backend.

## R3. Таблицы и audit (CHG-0219)

- R3.1: Нормализовать текущее domain state и refs. Mutation + audit + recovery
  witness атомарны по принятому R2; witnesses покрывают authorization state,
  не только sequence. Нет secret values в state/ledger/audit DTO.
- R3.2: Preserve contract digests, owners, contexts, IDs, idempotency fingerprints,
  sequence, grant/revoke/delete. Leases после restart отсутствуют.
- R3.3: Events выбираются по current actor scope, sequence и limit <=100.
  Нет полного b.events/Records clone в RAM. Старые sequence не переиспользуются.
  Expired cursor сообщает earliest available и gap; другой scope не получает
  foreign event. HTTP/OpenAPI/client amendment поставляется согласованно.
- R3.4: Истёкшие sessions/requests/OAuth metadata собираются bounded batches;
  replay tombstones сохраняются по explicit replay horizon. Целостность
  history проверяется потоково; непроверенное повреждение не скрывается.

## R4. Provider object lifecycle (CHG-0220)

- R4.1: Объект имеет current/pinned/retired/pending-delete/deleted state,
  operation ownership и timestamps; отдельные rows вместо растущего OwnedRefs.
  Новые versions добавляют ограниченные metadata, не весь список предыдущих.
- R4.2: Новая версия активируется после durable write, старая удаляется только
  после отсутствия pins и grace. Учитываются active use, quiesced restore,
  current recovery manifest и explicit backup policy.
- R4.3: Local creation intent фиксируется до primary/recovery object write;
  restart завершает/карантинирует свою операцию по точной identity.
  Внешний provider без recoverable ref не получает выдуманной гарантии.
- R4.4: GC удаляет только доказанно принадлежащие module refs; aliases и
  shared credential другого binding не трогает. Missing/unknown objects —
  report/quarantine, не prefix sweep. Повтор удаления idempotent.
- R4.5: Удаление всех managed copies и directory sync предшествуют receipt
  completed. Ошибка оставляет durable pending-delete и credential denied.

## R5. Объём и archives (CHG-0219/0199)

- R5.1: Audit hot age/bytes, archive bytes/files, objects, scratch и reserve
  учитываются вместе с recovery copies. Defaults/caps и retention semantics
  документируются и проверяются check-config; нет безлимитного значения.
- R5.2: При 80% начинается bounded maintenance. До hard ceiling резервируется
  максимальная следующая mutation и lifecycle completion/revoke.
  Exhausted reserve блокирует новое использование secrets; audit не отключается.
- R5.3: Archive имеет encrypted records, sealed manifest, sequence range,
  chain/digest и store identity. Source удаляется только после sync и verify.
  При отсутствии explicit destructive retention audit только экспортируется;
  заполнение приводит к видимому admission stop, а не скрытому удалению.
- R5.4: Metadata GC, provider GC и audit retention имеют разные predicates.
  Удаление audit не удаляет актуальные запреты или доказательства replay.

## R6. Миграция и приёмка (CHG-0226)

- R6.1: Read-only legacy importer, writers fenced, проверенная копия source,
  versioned manifest old head -> new identity, exact scope/ref/count сверка.
  Не использовать mutating Open/auto-compaction на оригинале как верификатор.
- R6.2: Stage -> verify -> atomic activation -> health/scoped status/revoke
  acceptance. Crash в каждом шаге допускает продолжение той же операции.
  Source сохраняется защищённым до принятой политики удаления.
- R6.3: После новых mutation/revokes rollback старого backup запрещён.
  Config/contracts/TLS/master/recovery keys имеют самостоятельный recovery
  runbook. Потеря primary volume отличается от потери Docker host.
- R6.4: Broker >=85% total и каждого production package, race/fuzz/quality,
  dependency checks и native Linux container gate обязательны.
  Fixture, container и live evidence маркируются раздельно.

## Acceptance

| ID | Сценарий и обязательное наблюдение |
|---|---|
| A1.1 | Grant at seq1, revoke at seq2; потеря latest anchor: отказ, исходные bytes не изменены, старый grant не активирован |
| A1.2 | Новый пустой store, interrupted first write, missing initialized head и stale valid head различаются |
| A2.1 | Fault matrix R2.3 на real engine/temp volumes; ни один ack не теряется при сохранном recovery contour |
| A2.2 | Wrong key/plaintext scan/temp files/engine version и actual PRAGMAs проверены без вывода материалов |
| A3.1 | Import сохраняет IDs/sequence/scopes; new mutation increment; deny survives restart |
| A3.2 | 1M synthetic audit events: hot-path памяти не O(history), Events читает страницу, auth не загружается replay всей истории |
| A3.3 | Expired/foreign cursor и altered authenticated old state не дают успешного доступа |
| A4.1 | 1000 refresh/checkpoints: последняя версия читается; pinned не удаляются; metadata не копируют всю историю refs |
| A4.2 | Crash before activation / delete failure / restart: known cleanup resumes, unknown and aliases intact |
| A5.1 | Burst + archives + recovery + scratch достигают полного budget раньше ENOSPC; revoke reserve сохраняется |
| A6.1 | Migration crash matrix и primary-volume restore сохраняют последний подтверждённый revoke |
| A6.2 | Module gates и root integration выполнены отдельно; unproven boundaries перечислены честно |

Performance A3.2 измеряется на 10k/100k/1M одинаковых synthetic events,
при одинаковых current rows/cache limits. Результаты с environment записываются
в CHG state; конкретные RAM/latency SLO фиксируются evidence gate до rollout,
без выдуманных абсолютных чисел для неизвестного диска/host.
