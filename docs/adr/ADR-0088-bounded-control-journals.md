---
description: "MVP: раздельные курсоры, receipts, аудит и диагностика с конечными бюджетами без миграции root services на БД."
last_verified: "2026-10-28"
---

# ADR-0088: Ограниченные управляющие журналы MVP

Status: accepted; implementation not started.
Дата решения: 2026-10-28.

Связи: [SPEC-0088](../../specs/active/SPEC-0088-bounded-control-journals.md),
[порядок реализации](../../.work/in-progress/CHG-0215-broker-recovery-head/README.md),
ADR-0015/0036/0048/0051/0055/0056/0082 и SPEC-0053/0081/0083.
Broker SQLCipher ограничен [ADR-0087](ADR-0087-broker-bounded-sqlcipher-store.md).

## Контекст

В control plane уже есть atomicJSON с directory sync, write-ahead stream
receipts и replay, файловые locks, spool retention, ToolHub conflict fence,
structured logging и bounded Hosting audit. Их надо закончить, не заменять
системой workflow. Смешанные файлы events, игнорируемые write/cleanup errors,
pending-only bytes ceiling и диагностический truncate создают отдельные дефекты.

## Решения

### D1. Файлы остаются substrate root plane

ToolHub сохраняет JSON snapshot, conflict fence и один authority owner.
Communication сохраняет существующий spool/atomicJSON/receipts.
Не вводятся root SQLite, DB queue, shared DB mount, workflow engine,
универсальный event bus, поисковая платформа или новый daemon.
Hermes memory/transcripts и artifacts сохраняют существующие owners.

### D2. У каждого класса данных свой reader и collector

Versioned layout Communication:
- event-state/: cursors, seen-set, source health, probe revision;
- run-events/: durable stream/outcome receipts;
- provider-dedup/: channel/provider replay evidence.

Имена — внутренний layout, не новая agent-visible семья tools.
Legacy events/ мигрируется под эксклюзивным spool writer, по строгому schema
discriminator. Неизвестный/повреждённый файл изолируется и диагностируется,
не трактуется как нулевой cursor и не удаляется общей уборкой.
Перенос idempotent, sync/manifest защищают crash; старая версия layout
отклоняется writer, а не silently mixed.

Живой/paused subscription cursor — состояние, не terminal history.
Run receipt с незавершённым handoff/approval и dedup внутри replay window
защищены от очистки. Отсутствие ранее созданного cursor означает visible gap,
если нельзя доказать безопасный bootstrap. Revision change не маскирует gap.

### D3. Долговечный receipt предшествует производным записям

У результата запуска есть один canonical receipt: identity + outcome +
точное намерение доставки. Mapping/outbox восстанавливаются его повторным
применением с тем же ID. Existing streamReceipt расширяется или переиспользуется;
второй общий transaction engine не пишется.

Для source batch durable admission intent фиксирует source identity/revision,
границы cursor, bounded event IDs, filter decisions и детерминированные sink IDs.
Записывается до применения sink. Cursor продвигается после завершения handoff;
replay применяет те же IDs. Незавершённый batch не prunable.
Для consuming source гарантии ограничены его реальным ACK/replay contract;
предпочитается уже существующий non-consuming cursor API.

### D4. Ошибка после эффекта — не отказ до эффекта

Идентичность пользовательской операции сохраняется между attempts.
Автоповтор допускается при доказанном отсутствии эффекта или явном idempotent
contract; annotations/проза модели не являются таким доказательством.
Timeout, audit/checkpoint failure после backend call и потерянный ответ
не позволяют переиграть всё действие новым job ID. OAuth refresh retry
требует доказанного auth rejection до исполнения, а не любого transport error.
Где такого доказательства нет, сохраняется uncertain и идёт сверка.

Различаются authorization decision, execution outcome и delivery outcome.
Allow не означает business success, backend error не означает authorization deny.
User-visible result и audit не объявляют неизвестный исход успехом.

### D5. Обязательный аудит и диагностические логи имеют разные гарантии

internal/audit получает finite segments, byte/record bounds, cross-process
writer lock для append/rotation, sync и ограниченное scoped чтение.
До эффекта durable admission и резерв completion; неудача записи результата
оставляет незавершённую операцию для recovery и останавливает слепой retry.
SSH/exposure и другие callers перестают молча игнорировать обязательный audit.
При callback cleanup/revoke нельзя блокировать прекращение доступа отсутствием
места для новой операции: для lifecycle заранее выделяется резерв.

Используется опыт internal/hosting/audit.go; бизнес-модуль Hosting не становится
dependency общего audit. Поля allowlisted у producer; regex redaction — второй
барьер. Не сохраняются prompts, полные args/results, auth links или secret keys.

Diagnostics остаются best-effort: rotation вместо O_TRUNC, source identity +
parsed timestamp/tie position, данные до cursor, bounded streaming reads.
Пропуск, source retention loss, unreadable stopped container и corrupt cursor
показываются явно. Trace scope определяется authority records, не строкой лога.

### D6. Полный бюджет и обслуживание у владельца данных

Bytes/files accounting включает все owned dirs, copies, archives, journals и
scratch. Не считать один pending/ или один ledger всем footprint.
Начало уборки — 80% configured budget; новые admissions ограничиваются до hard
ceiling с учётом максимальной следующей записи и lifecycle reserve.
Есть bounded reconciliation счётчиков, персистентные ошибки maintenance и
запрет повторять cleanup бесконечно без backoff.

Существующие retention windows сохраняются, если эта SPEC явно не меняет их.
Предлагаемый default диагностик — 7 дней плюс конечный byte cap.
Для security audit нет нового молчаливого TTL: destructive policy задаётся
оператором; отсутствие policy означает export-only и eventual admission stop.
Tombstones и dedup живут не меньше доказанного replay window; active/uncertain
не удаляются по возрасту. Store corruption не является мусором.

Новый operator CLI ограничен расширением doctor и storage status/cleanup
--dry-run. --apply работает только по свежему scoped plan digest, при нужном
writer lock и explicit retention policy; не удаляет неизвестные paths.
Это не agent tool, не новый API управления providers и не unscoped Docker prune.

### D7. Существующие lifecycle paths ремонтируются точечно

ToolHub Save syncs parent directory; startup не заменяет потерянный initialized
store пустым. Onboarding not-found имеет безопасные причины, а не догадку о
потере volume. Confirmation приходит от trusted human callback, связывает actor,
scope, action digest, revision, expiry и потребляется один раз.

Desired/applied projection revision и semantic tool-surface digest переживают
restart; startup делает reconcile вместо примирования RAM baseline.
Несколько обновлений coalesce, причина перезапуска сохраняется.
Revocation действует немедленно на ToolHub; soft restart ждёт безопасной границы.
Model relay сохраняет существующие configured fallback/cooldown; title error
не меняет выполненный job, outage записывается отдельной причиной.

Любой agent-tool contract change включает descriptions, prompt_contract,
docs/tools.md, docs/routines.md, новый immutable catalog и rebind + respawn.

## Нецели и миграции

Нет silent migration. Legacy layouts читаются/переносятся проверяемой
одноразовой процедурой; после format switch старые writers запрещены.
Не повышаем минимальные grants и не отключаем Origin/CSRF/TLS/audit.
External exactly-once, distributed jobs и full-host HA не добавляются.
Изменения docs/ текущей системы выполняются по фактической реализации;
этот ADR не утверждает, что новый layout уже работает.
