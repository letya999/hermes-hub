---
description: "Принятое дополнение: операционное хранение ToolHub и Communication переходит на SQLite; уточняет контур SQLCipher Broker и общий контракт миграции."
last_verified: "2026-10-31"
---

# ADR-0091: SQLite-хранилище ToolHub и Communication

Status: accepted — SQLite stores delivered (CHG-0215–0226), verified 2026-11-15.
Дата решения: 2026-10-31.

Связи: [SPEC-0090](../../specs/active/SPEC-0090-root-services-sqlite.md),
[ADR-0087](ADR-0087-broker-bounded-sqlcipher-store.md),
[ADR-0088](ADR-0088-bounded-control-journals.md),
[порядок реализации](../../.work/done/CHG-0215-broker-recovery-head/README.md).
Дополняет ADR-0087/0088 и SPEC-0087/0088; тексты принятых ADR и frozen SPEC
не редактируются — это решение изменяет отдельные пункты явно.

## Контекст

ADR-0087 исключил миграцию ToolHub/Communication на SQLite («исключена из
данного решения»), ADR-0088 D1 закрепил файлы как substrate root plane.
Владелец поставил новую цель: полный переход операционного хранения всех
трёх сервисов — Broker → SQLCipher/SQLite, ToolHub → SQLite,
Communication → SQLite — с проверяемой миграцией и dev-приёмкой.

Файловый substrate после импортированных исправлений остаётся уязвим к
классам дефектов, которые решает транзакционный движок: rename-цепочки
как state machine без межзаписной атомарности, линейные ReadDir-сканы,
per-file fsync-стоимость при burst, ручная сверка mapping/by-key индексов,
диагностический footprint из тысяч мелких файлов на томе.

## Решения

### D1. Изменение принятого исключения

Пункт «Миграция ToolHub/Communication на SQLite: исключена» в разделе
альтернатив ADR-0087 и D1 ADR-0088 («Не вводятся root SQLite») отменяются
для двух названных сервисов. Остальные запреты ADR-0088 D1 сохраняются:
нет DB queue/workflow engine/shared DB mount/event bus/нового daemon.

ToolHub и Communication переходят на SQLite через `database/sql` и
pure-Go драйвер (modernc.org/sqlite). CGO/native build для root
бинарников не вводится — ограничение ADR-0087 D1 остаётся в силе,
поэтому SQLCipher для root DB не применяется (см. D4).

### D2. По одной embedded БД на сервис на space

- ToolHub: `toolhub/store.db` рядом с прежним `store.json` в `/state/toolhub`.
- Communication: `spool.db` в корне spool volume (`/data`).
- Broker: primary/recovery/witness SQLCipher DB по ADR-0087 D3 в тех же
  трёх named volumes.

БД не разделяется между сервисами и не монтируется в agent/runtime.
Dev/prod разделены существующими per-env volumes; БД никогда не копируется
между env. Один процесс-владелец на БД; межпроцессный fence сохраняется
(существующий flock/writer.lock + busy timeout движка).

### D3. Схемы по классам записей, а не ORM

ToolHub сохраняет snapshot-модель: коллекции сущностей пишутся как строки
`(collection, id, payload)` + метаданные (installation, schema, digest,
generation, initialized) одной транзакцией, сохраняя прежнюю семантику
atomic snapshot + conflict fence. Audit сервиса остаётся в internal/audit
finite segments по SPEC-0088 R3 — это принятый bounded substrate; в БД
уходит только transaction-scope audit Broker (SPEC-0087 R3.1).

Communication хранит записи очереди/курсоров/receipts/dedup/mappings/
callbacks/budget/usage в таблицах того же вида: запись = прежний JSON
document, ключ = прежний (dir, name), состояние очереди = колонка dir;
rename-переходы становятся UPDATE внутри транзакции. Бинарные payload
(delivery blobs), `tmp/`, архивы `.jsonl.gz` и `mock-*` остаются файлами —
это payloads и архивы, не операционное состояние. Routines engine
(schedules/occurrences/watchers/digests/assignments/hooks/goals) получает
тот же record substrate через расширение существующего Deps-шва.

Broker реализует SPEC-0087 R3 как принято: нормализованные таблицы
requests/sessions/credentials/grants/state/oauth/contracts + hash-chained
events + witness, mutation+audit+witness атомарны.

### D4. Защита данных по модели угроз

Broker: SQLCipher с независимыми ключами primary/recovery/witness —
без изменений (ADR-0087 D1/D3). Secret values в таблицы не входят.

ToolHub/Communication: шифрование страниц БД недоступно без CGO, поэтому
граница защиты — per-space volume 0700 + file mode 0600, как сегодня для
`store.json`/spool. Полей с secret material в этих БД нет: credential
references остаются opaque refs, token files и sealed credstore лежат
вне БД, Telegram-сессии канала не переносятся и не копируются в records.
Поля, уже sealed сегодня (callbacks и др.), остаются sealed payloads.
Любое будущее поле с secret bytes обязано идти в существующие encrypted
stores, а не в эти БД.

### D5. Явная однонаправленная миграция для всех трёх

Единый контракт per service: fence writers (остановленный сервис +
writer lock) → проверенная read-only копия source → импорт без изменения
источника → сверка IDs/counts/scopes/events → versioned manifest
old head/snapshot → new store identity → atomic activation marker →
health/revoke acceptance → scoped removal старого layout только после
успешной dev-приёмки и operator `--apply`.

Activation записывает marker, который старый бинарник отвергает
(новый layout/schema version); старый writer не может молча создать
пустой legacy store рядом с активированным. Crash на любом шаге
продолжает ту же операцию по manifest, не начинает новую.

Импорт Communication: каждый прежний `(dir,name)` JSON record → строка;
jobs mappings/in-flight инварианты перепроверяются после заливки.
Импорт ToolHub: последний валидный `store.json` + `.initialized` →
транзакционная заливка коллекций; digest записывается в manifest.
Импорт Broker: journal records → events с сохранёнными sequence/hash,
domain tables строятся replay импортера; P/R/anchor согласованность
проверяется до записи (переиспользует CHG-0215 head-migration rules).

### D6. Бюджеты и recovery сохраняются

SPEC-0088 R5 применяется к новому substrate: bytes/files учёт включает
DB file, rollback/WAL sidecars, scratch миграции и оставшиеся payload
files. Journal mode по сервису: Communication WAL + synchronous FULL
(один writer, читателей нет — WAL выбран ради checkpoint контроля и
single-file recovery; hot WAL bounded через checkpoint при close и
periodic maintenance); ToolHub то же; Broker — DELETE + EXTRA по
ADR-0087 D3 для multi-DB atomicity, без исключений.

Startup recovery: отсутствующий initialized/marker на инициализированном
store — отказ, не пустой bootstrap; corrupt DB — refusal с quarantine
manifest, не silent re-create; hot journal/WAL решает сам движок,
ручного удаления sidecars нет.

## Альтернативы

- Оставить файлы и достроить D2–D7 ADR-0088: сохраняет rename-цепочки и
  per-file fsync стоимость; не отвечает цели владельца.
- Один общий root DB на все сервисы: shared mount/cross-service coupling,
  запрещён границами и ADR-0088 D1 (shared DB mount).
- SQLCipher для root сервисов: требует CGO в root binaries — запрещено
  ADR-0087 D1; защита по D4 покрывает актуальную модель угроз.
- Нормализация всех полей вместо `(collection,id,payload)`: не меняет
  доступы (keyed lookups, dir-scoped scans), умножает diff и риск.

## Ограничения

- Однофайловая БД на одном volume не является HA; потеря volume =
  потеря store (как и сегодня), recovery для Broker — по ADR-0087.
- `(collection,id,payload)` оставляет JSON-валидацию в коде; схема не
  заменяет domain validation.
- Длинные JSON payloads в records не индексируются по содержимому —
  чтение остаётся по ключу/директории, как сегодня по имени файла.
- WAL checkpoint и bytes accounting — обязанность maintenance сервиса;
  неограниченный WAL growth недопустим.

## Первичные источники

- [SQLite atomic commit](https://www.sqlite.org/atomiccommit.html).
- [SQLite WAL](https://www.sqlite.org/wal.html).
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite).
