---
description: "Принятое направление: ограниченное SQLCipher-хранилище брокера с проверяемым восстановлением и явной миграцией."
last_verified: "2026-10-28"
---

# ADR-0087: Ограниченное хранилище Credential Broker

Status: accepted direction; implementation and SQLCipher recovery gate not started.
Дата решения: 2026-10-28.

Связи: [SPEC-0087](../../specs/active/SPEC-0087-broker-bounded-storage.md),
[порядок реализации](../../.work/in-progress/CHG-0215-broker-recovery-head/README.md).
Дополняет ADR-0060/0063/0064/0066 и SPEC-0064/0065/0066/0067/0068.
ADR-0063 остаётся описанием работающего journal до проверенной миграции.
Старые принятые ADR и hash-pinned SPEC-0064 не редактируются.

## Контекст

Hub — локальный appliance с небольшим Go control plane. Broker — отдельный
Go module/process, авторизующий выдачу secret material через providers.
В ходе read-only аудита обнаружены полный replay и хранение аудита в RAM,
два растущих ledger, отдельный commit-файл на событие и накопление OwnedRefs.
Точный снимок кода, ограничения доказательств и источники находятся в research.md
первого CHG; это не утверждение о текущем prod.

Реальная граница пересмотра ADR-0063 наступила: объём, retention и migration.
Простое сжатие не ограничивает число событий, количество файлов и память.
Удаление audit-only записей с перенумерацией нарушает cursors и recovery.

## Решения

### D1. Один встроенный движок внутри брокера

Целевое хранилище — SQLCipher Community с проверенным поддерживаемым Go binding.
Один процесс/writer; явные SQL migrations; без ORM, универсального backend API,
отдельного SQL-сервера и сети репликации. Поддержка CGO/native build допустима
только в модуле и образе Broker; root hubctl/control binaries сохраняют свой
действующий режим сборки. Версии движка, binding и crypto library фиксируются
в CHG-0218 после воспроизводимой проверки, до начала CHG-0219.

Секретные значения, OAuth tokens, PEM и payload файлов НЕ входят в таблицы
ledger/state/audit: в них только разрешённые DTO и opaque provider references.
Encryption не даёт разрешения сохранять секреты в ledger.

### D2. Текущее состояние не восстанавливается из всей истории

Нормализованные таблицы содержат requests, browser sessions, credentials,
grants, state checkpoints, OAuth metadata, dynamic contracts, object references
и задания очистки. Таблицы credential/state не содержат растущих OwnedRefs JSON.
Одна транзакция фиксирует изменение metadata и соответствующий audit event.
Leases и plaintext materializations остаются ephemeral.

Audit имеет прежний монотонный sequence, ограниченные страницы и scoped reads.
Старые sequence/IDs при импорте не меняются. Чтение и старт не создают копии всей
истории в RAM; проверка целостности может читать её потоково.

### D3. Recovery — обязательный контракт, не фоновый backup

Подтверждённый commit должен переживать потерю primary при сохранных независимых
recovery и witness. Witness связывает store identity, schema, commit sequence,
актуальное authorization state и audit tip. Он имеет отдельный purpose/key.
Наличие самого большого оставшегося commit-файла не является доказательством
последнего подтверждённого состояния.

До SQLCipher исправляется текущий journal: versioned sealed current-head,
явный genesis, atomic replace + file/directory sync, missing-head refusal.
Миграция legacy head разрешена только на остановленном, согласованном наборе;
неразличимый tail не объявляется неподтверждённым только по отсутствию файла.
Старый бинарник должен отвергать новый initialization/version marker.
Никакого fallback к предыдущему head при отсутствии текущего.

Кандидат для SQLCipher: primary DB + recovery DB + компактная witness DB,
обновляемые одной ATTACH-транзакцией. Recovery DB открывается как main,
чтобы super-journal оставался в recovery-контуре. Все участвующие DB используют
rollback journal DELETE и synchronous EXTRA; независимые ключи задаются явно;
temp storage не оставляет plaintext на диске. Это проверяемая конструкция,
а не доказанный результат применения к Docker volumes.

CHG-0218 обязан проверить kill/crash, ENOSPC, потерю primary до/во время/после
commit, hot journals, разные keys, stale copy и ответ, потерянный после commit.
Без положительного результата CHG-0219 не начинается. Если gate не проходит,
остаётся исправленный journal; независимые root-plane исправления продолжаются.
Нельзя ослабить recovery или добавить самописную mirrored-WAL схему вместо gate.

### D4. Secret objects имеют явный жизненный цикл

Provider boundary сохраняется. Текущий ref, закреплённые активной операцией refs,
retired refs и pending delete учитываются отдельно. Сначала закрепляется новая
версия, затем старая допускается к удалению после освобождения pins и grace.
Quiesced checkpoint сохраняет только действительно нужные версии.

Local provider получает operation-bound учёт создания до записи новых объектов,
чтобы сбой до активации оставлял проверяемую незавершённую операцию.
Для внешних providers без заранее известного ref нельзя обещать отсутствие
orphans после потерянного ответа. Неизвестные пути — report-only; никакого
удаления по prefix, mtime или догадке. External aliases не принадлежат GC Broker.
Deletion подтверждается после удаления всех управляемых recovery copies;
ошибка сохраняет pending-delete и deny, а не возвращает credential в active.

### D5. Retention ограничивает историю, не authorization

Audit архивируется целыми проверяемыми диапазонами с sealed manifest,
границами sequence и проверяемой связью с текущим store. Export/rotation не
позволяют вернуть grants, отменённые после начала диапазона.
Истёкшие enrollment metadata очищаются, но tombstones/idempotency evidence
живут не меньше согласованного окна replay.

Срок удаления security audit не придумывается по ходу уборки: действует
operator policy. При отсутствии explicit destructive retention удаление
аудита запрещено, разрешены export/архивация. При исчерпании полного бюджета
новая работа ограничивается, обязательный аудит не отключается.
Bytes/files budget включает primary, recovery, archives, object copies,
journals и migration scratch; есть резерв завершения и revoke.

### D6. Явный однонаправленный переход

Мигратор читает legacy source без изменения. Нужны остановка/fence writers,
проверенный backup, согласованные ключи и manifest old head -> new store.
Проверяются ownership, grants/revokes, IDs, sequence, refs и scoped events.
Исходник остаётся для сверки, но старый бинарник не пишет в новый store.
После новых revokes нельзя возвращать старый backup как обычный rollback.
Backup включает config, contracts, TLS и keys: одна metadata DB их не заменяет.

## Альтернативы

- Оставить compression и поднять лимит: не решает RAM, inode growth и old refs.
- Два самописных журнала state/audit: усложняет atomicity, cursors и recovery.
- SQLite без encryption: раскрывает конфиденциальные metadata Broker.
- PostgreSQL: оправдан при multi-host writers/HA, отсутствующих в этом MVP.
- DuckDB/TypeDB/vector extensions: другие задачи; не заменяют этот контракт.
- Миграция ToolHub/Communication на SQLite: исключена из данного решения.

## Ограничения и доверие

SQLite multi-file atomicity не доказывает всю выбранную failure model.
SQLCipher page integrity сама по себе не обнаруживает rollback старых валидных
страниц/целой DB; поэтому проверяется current-state witness.
Одновременный rollback всех доверенных копий без внешнего источника свежести
не определяется. Несколько volumes одного Docker host не являются HA.
Ни один этап не обещает внешних эффектов exactly-once без idempotent sink.

## Первичные источники

- [SQLite ATTACH](https://www.sqlite.org/lang_attach.html).
- [SQLite atomic commit](https://www.sqlite.org/atomiccommit.html).
- [SQLCipher API](https://www.zetetic.net/sqlcipher/sqlcipher-api/).
- [SQLCipher design](https://www.zetetic.net/sqlcipher/design/).
