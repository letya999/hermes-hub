---
description: "Замороженные требования к курсорам, receipts, аудиту, диагностике и бюджетам существующего control plane."
last_verified: "2026-10-28"
---

# SPEC-0088: Bounded control journals MVP

Frozen: 2026-10-28. Design accepted; implementation not started.
Решение: [ADR-0088](../../docs/adr/ADR-0088-bounded-control-journals.md).
План: [CHG-0215..0202](../../.work/in-progress/CHG-0215-broker-recovery-head/README.md).
Дополняет SPEC-0030/0034/0053/0055/0076/0081/0083.
Никакого перехода ToolHub/Communication/Hermes на новую БД.

## R1. Раздельный layout и безопасная уборка (CHG-0216)

- R1.1: Cursor state, run receipts и provider dedup имеют отдельные versioned
  directories и schemas. Legacy migration под writer lock idempotent, crash-safe,
  unknown data quarantined/report-only, старый writer не смешивается с новым.
- R1.2: Active/paused/snoozed source cursor не terminal history. GC cursor —
  только после удаления source и истечения согласованного replay horizon.
  Pending deliveries/approvals/uncertain и незавершённые receipts защищены.
- R1.3: Потерянный initialized cursor и unexpected probe revision дают gap,
  не silent zero/bootstrap. Чужой scope/revision не наследует cursor.
- R1.4: Archive append sync/verify предшествует удалению source. Частичный
  gzip member, crash after append и repeated compaction не теряют source;
  archive dedup идентифицируется стабильным record ID.

## R2. Диагностические логи (CHG-0217)

- R2.1: O_TRUNC replacement прекращается: active file + finite rotated files,
  size/age/count caps; host collector следит за file generation, не одним offset.
- R2.2: Получение порций идёт от oldest unread, bounded bytes/time.
  5000/5001 и одинаковый RFC3339Nano timestamp не пропускают первые записи.
  Cursor фиксируется после data sync; повтор допустим и различим.
- R2.3: Source rotation, unreadable stopped container и corrupt cursor дают
  explicit gap/error. Они не объявляются fully drained.
- R2.4: Trace использует выбранный env/home resolver, exact structured IDs,
  authority-derived owner/context, dedup и ограниченный output/deadline.
  Неизвестный owner не разрешает читать все Docker containers.
- R2.5: Producer allowlist, no content by default, no credentials/auth links,
  cap line bytes. Diagnostics failure не останавливает agent job.

## R3. Обязательный control audit (CHG-0221)

- R3.1: Finite segments; cap bytes/records; append/rotation имеют общий
  inter-process lock, file+directory sync и scoped bounded reader.
- R3.2: Authorization decision, execution outcome и delivery outcome различимы,
  связаны operation/job/run/tool-call IDs. Allow не business success.
  Backend error не authorization denial и не доказательство отсутствия эффекта.
- R3.3: Durable admission + reserved completion до эффекта; post-effect audit
  failure оставляет durable unsettled operation и uncertain, исключает retry.
  SSH/exposure callers не игнорируют required audit errors.
- R3.4: Audit поля разрешаются DTO/producer contract; raw args/results,
  transcripts, auth-bearing URLs и secret dictionaries запрещены.
  Unknown version/corruption не принимаются молча как пустой audit.
- R3.5: Retention не уничтожает unsettled intents или replay evidence.
  При отсутствии destructive policy — export-only и visible admission stop.

## R4. Jobs, delivery и source handoff (CHG-0222)

- R4.1: Existing receipt pattern используется для всех terminal paths;
  outcome + exact delivery intent сохраняются одним canonical record.
  Mapping/outbox восстанавливаются без повторного исполнения job.
- R4.2: Source batch intent содержит owner/source/revision, cursor interval,
  filter verdicts и deterministic sink IDs. Сохраняется до fanout; cursor —
  после durable handoff. Overflow/ошибка оставляют replayable remainder.
- R4.3: Operation identity стабильна между attempts. -r1 не создаёт новое
  разрешение. Auto retry только до эффекта либо по verified idempotent contract.
  Auth refresh не повторяет side-effecting call после неопределённой ошибки.
- R4.4: Pause/delete/assignment generation/revoke проверяются у effect point.
  При неизвестном результате send идёт reconcile, не новое blind send.
  Внешнее exactly-once без provider idempotency не заявляется.
- R4.5: Ошибки mandatory save возвращаются и сохраняются как unhealthy;
  existing maintenance/recovery loops bounded и наблюдаемы.

## R5. Полный footprint и operator maintenance (CHG-0223)

- R5.1: Accounting включает все owned dirs/copies, artifacts, archives, indexes,
  DB journals и scratch; byte ceiling не ограничивается pending/.
  Есть max records/files, max next write и reserve lifecycle операций.
- R5.2: High water 80%; bounded cleanup + backoff; data-owner predicates из
  R1/R3 и SPEC-0087 имеют приоритет над age. No broad filesystem/Docker prune.
- R5.3: Doctor/storage status показывает только safe aggregates per env/scope:
  bytes/files, available budget, oldest retained boundary, pending cleanup,
  last error и store/image/catalog revisions. Не печатает object names/keys.
- R5.4: Cleanup --dry-run выдаёт bounded scoped plan digest без secret paths.
  --apply перепроверяет generation, policy, scope и writer fence; stale plan
  отказывается действовать. Unknown object/delete policy — report-only.
- R5.5: Существующие ToolHub/spool retention windows сохраняются. Diagnostics
  default 7 days плюс finite bytes/files. Финитные defaults и config validation
  задаются в owners; миграционный scratch считается отдельно до activation.

## R6. ToolHub state и human confirmation (CHG-0224)

- R6.1: Save сохраняет file+parent directory, conflict fence и lock.
  Initialized missing/malformed snapshot не создаёт пустой успешный Store.
  Recreate сохраняет connections/onboardings/broker refs без нового credential entry.
- R6.2: Safe not-found reason различает unknown ID, scope, expired, terminal,
  version conflict и storage unavailable; значения секретов и auth links
  не входят в diagnostics. External error privacy сохраняется.
- R6.3: Confirmation только trusted human channel, bound actor/scope/action
  digest/revision/expiry, single-use atomic consume. Agent видит awaiting-user
  и не получает возможности превратить собственный вызов в согласие человека.
- R6.4: Все agent-visible contract changes проходят полную связку descriptions,
  prompt_contract, tools/routines docs, fresh immutable catalog и rebind/respawn.

## R7. Projection/runtime causes (CHG-0225)

- R7.1: Desired/applied revision + semantic surface digest + reason durable.
  Restart ToolHub не теряет pending reconcile. Только semantic changes вызывают
  soft restart; burst coalesces. Revocation не ждёт restart.
- R7.2: Каждый stop/restart имеет причину/operation ID/generation в logs/audit;
  planned rebuild, warm-idle stop и fault различимы.
- R7.3: Model outage typed; existing configured cooldown/fallback сохраняются.
  Title generation не меняет terminal job. Error tool result не становится
  false completed; проверяется pinned Hermes, без silent upstream patch.

## R8. Acceptance и безопасный rollout (CHG-0226)

- R8.1: Отдельные fixture/container/live receipts; no mock-as-live.
  Никакого external send/login/provider mutation без соответствующей инструкции.
- R8.2: Layout migration crash-safe, исходник сохранён, старый writer fenced.
  Рабочий dirty tree CHG-0214 не перезаписывается.
- R8.3: Root just check; own Go >=85%; broker gate отдельно; security при deps,
  Docker через hubctl/just. Каждый CHG имеет actual evidence и honest limitations.

## Acceptance

| ID | Сценарий и обязательное наблюдение |
|---|---|
| A1.1 | Paused source с aged cursor + backlog сверх file cap: Compact не меняет cursor; resume не bootstrap |
| A1.2 | Layout migration crash/unknown schema/foreign scope: safe refusal или resume; чужие records intact |
| A1.3 | Archive write/close/sync failure: исходный terminal record не удалён |
| A2.1 | 5000/5001 строк, equal timestamps, host rotation и data/cursor crash: no silent skip |
| A2.2 | Unknown owner и foreign job IDs: trace не читает чужую историю |
| A3.1 | Audit unavailable до эффекта: backend не вызван; после эффекта: uncertain, повтор отсутствует |
| A3.2 | Parallel append/rotate + disk full: readable segments, bound footprint, unsettled intents retained |
| A4.1 | Crash receipt->mapping->outbox и duplicate event: тот же result/delivery, job не повторён |
| A4.2 | Burst 10, sink cap8; restart/filter error/pause: остаток2 replayable, cursor не перескочил |
| A4.3 | Job/tool/OAuth timeout after effect: no -r1 replay; verified pre-effect transient может повториться |
| A5.1 | Малый pending + большой outbox/archive/recovery: ceiling учитывает полный footprint |
| A5.2 | Stale/cross-scope cleanup plan и symlink: отказ, unknown/live data intact |
| A6.1 | Recreate + concurrent Save + missing initialized store: сохранность либо явный отказ без нового пустого store |
| A6.2 | Agent-confirm/replay/expired/wrong actor/action: denied; exact human confirmation consumed once |
| A7.1 | Restart notifier до применения и burst projection updates: reconcile один раз по latest semantic revision |
| A7.2 | Revoke during active run и model/title outage: доступ закрыт; job/result semantics не искажены |
| A8.1 | Full staged rollout + restart + storage pressure; каждый этап имеет различимые реальные receipts |
