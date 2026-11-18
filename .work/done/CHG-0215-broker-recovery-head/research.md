# Основание Storage reliability MVP

Дата: 2026-10-28. Метод: read-only review исходников, текущих docs и двух
прикреплённых владельцем отчётов; предыдущие безопасные aggregate measurements.
Дизайн подготовлен в branch fix/m23-live-wake.
Baseline HEAD: ae1dcf6344869e75f64b003f6aa6b6164eedc451.
До подготовки пакета: 84 modified/untracked paths. Это working-tree audit,
не аудит одного immutable commit и не доказательство prod readiness.

## Приоритетные findings

| ID | Источник | Наблюдение / следствие | Проверка будущего CHG |
|---|---|---|---|
| F1 | journal/recovery.go latest/openReplicated | Max remaining commit file -> truncate P/R; loss latest может выглядеть как uncommitted tail и откатить revoke | 0191: synthetic grant+revoke, lost head |
| F2 | journal/journal.go Records; broker.go New/apply/Events | Full history/raw records/copies/events в RAM и replay; линейный по истории working set | 0194/0195 scale gate |
| F3 | oauth.go AccessToken; runtime.go Release | OwnedRefs append, вся растущая запись снова сериализуется; cumulative journal metadata может расти квадратично по числу версий | 0196 normalized refs + known GC |
| F4 | provider/local.go Write/Delete; recovery docs | Объекты P/R создаются по новому locator; known versions удаляются при DeleteCredential, expired/unknown orphans не имеют автоматического GC | 0196 ownership/retirement |
| F5 | communication/spool.go Compact; events.go eventPath | Source state/run receipts/provider dedup в events/; nil Delivery считается archivable. Старый paused cursor уязвим общей уборке | 0192 typed directories |
| F6 | spool.go appendArchiveLocked | Gzip close/file close без file/dir sync перед remove(source) | 0192 durable archive |
| F7 | gateway_delivery.go | Separate RecordOutcome/EnqueueDelivery/CompleteJob, ignored errors, -r1 без effect evidence | 0198 existing receipt reuse |
| F8 | streams.go/events.go | Sink effects + cursor/state отдельно, ignored atomicJSON errors; replay недостаточно только seen-set | 0198 batch intent |
| F9 | budget.go checkCeilingsLocked | Bytes считает pending/, не full spool; control.go игнорирует Compact error | 0199 full accounting/maintenance |
| F10 | audit/audit.go | Fsync есть; unbounded append и full List в memory; нет общего cap/rotation | 0197 |
| F11 | sshcap/exposure audit callers | Required audit error местами discarded | 0197 before/after effect |
| F12 | toolhub/gateway_call.go | Admit до call есть; backend-error маркируется deny; post-effect checkpoint/audit error может выглядеть retryable; OAuth path повторяет после общего callErr | 0197/0198 typed outcomes/retry |
| F13 | diagnostics collector/host/trace | Last-5000, stamp<=last, truncate-on-cap; cursor durability gap, incomplete env paths/substring IDs | 0193 |
| F14 | toolhub/store_io.go | Lock+digest+temp fsync+rename есть; parent sync не найден; initialized missing store надо проверить | 0200 |
| F15 | restart_notify.go | RAM seen baseline и request без persisted desired/applied receipt | 0201 |

F1 — статический failure scenario, не воспроизведённый здесь exploit.
Существующие tests проверяют uncommitted tails, wrong keys, corrupt anchors;
они не доказывают, что missing latest подтверждённого commit безопасен.
Новое head состояние должно различать explicit genesis и потерянные данные.

## Исторические безопасные измерения

В раннем read-only окне этой сессии (dev, не prod):
- primary ledger: 128617584 bytes; recovery ledger столько же;
- commit files: 195869; recovery allocation около 898 MiB;
- Broker RAM: один snapshot около 481 MiB.

Это не новые измерения при записи документов и не утверждение, что все bytes
относятся к secret objects. Перепроверять только aggregate size/count и selected
service identity. Никогда не перечислять ключи credential dictionaries.

## Что уже есть и должно сохраниться

- communication atomicJSON: temp write, file Sync, rename, directory sync.
- streamReceipt: write-ahead event+delivery intent и recovery application.
- ToolHub flock/digest conflict fence, terminal operation/onboarding compaction.
- Spool time/count/archive retention, budget ledger и non-consuming cursor API.
- Hosting audit: admission, completion reserve, safe payload, recovery/retention.
- Runtime cold-wake retry уже учитывает sawTool; relay имеет cooldown/fallback.
- Hermes pinned dependency уже хранит sessions/messages в SQLite.

## Исправления выводов исходных отчётов

- Не доказано, что ToolHub лишён volume или что not-found = потеря credentials.
- Outbox empty и несколько доставленных сообщений не доказывают отсутствие потерь.
- Long polling/event stream не означает полного отсутствия polling.
- SIGTERM может быть planned rebuild/idle/tool projection, нужны причины.
- Lease audit нельзя удалить только потому, что leases не replay после restart.
- Сжатие journal lossless, но не решает lifetime/history/RAM/file-count limits.
- Старый предложенный compact с перенумерацией и SeedReplicatedFromOpen опасен:
  меняет audit cursor и не удовлетворяет recovery prefix/head contract.
- SQLite не отменяет GC, limits, encryption, replay identity и unknown outcomes.

## Prior art и первичные источники

В сессии рассмотрены 42 GitHub проекта, глубина не одинакова. Ниже только
релевантные решения; таблица сравнений не считается production benchmark.

- [NanoClaw DB](https://github.com/nanocoai/nanoclaw/blob/main/docs/db.md):
  separate owners/mailboxes; WAL не универсален на container mounts.
- [IronClaw event store](https://github.com/nearai/ironclaw/blob/main/crates/events/ironclaw_event_store/README.md):
  отделение аудит/проекция/transport; часть подробных contracts draft.
- [OpenClaw disk budget](https://github.com/openclaw/openclaw/blob/main/src/config/sessions/disk-budget.ts):
  общий объём DB/transcripts/archives и защита active data.
- [Eve notifications](https://github.com/vercel/eve/blob/main/docs/patterns/durable-cross-channel-notifications.md):
  durable outbox, lease и ограничение exactly-once для external effects.
- [Osinara](https://github.com/nyxandro/osinara): текущий core собственный,
  PostgreSQL/lease/HITL; старое краткое описание «на Eve» устарело.
- [GoClaw session management](https://github.com/roelfdiedericks/goclaw/blob/master/docs/session-management.md):
  LLM summaries сохраняют originals; это не disk retention.
- [SQLite ATTACH](https://www.sqlite.org/lang_attach.html) и
  [atomic commit](https://www.sqlite.org/atomiccommit.html):
  документированная multi-file atomicity при условиях rollback mode.
- [SQLCipher API](https://www.zetetic.net/sqlcipher/sqlcipher-api/) и
  [design](https://www.zetetic.net/sqlcipher/design/):
  independent attached keys, encrypted pages/journals, temp plaintext caveat.
- [Hearsay preprint](https://arxiv.org/html/2609.32495v1):
  self-authored harness log не доказывает полноту. Не general proof всех harnesses.
- [CapLease preprint](https://arxiv.org/html/2608.01710v1):
  stable authorization state; idempotent external sink требуется отдельно.

## Не проверено

Новый store, head-v2, native Go binding, ATTACH volume-loss protocol, GC и новый
layout не реализованы и не тестировались. Точный engine pin, resource baseline
и production restore требуют CHG-0218/0202. Одновременная потеря/rollback
всех доверенных copies без external freshness остаётся вне обещанной гарантии.
