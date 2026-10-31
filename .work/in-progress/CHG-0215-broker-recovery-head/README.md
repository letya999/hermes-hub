# Storage reliability MVP — вход для реализации

Дизайн принят: 2026-10-28. Production implementation: not started.
Это пакет проектирования по итогам read-only аудита. Подготовка документов
не означает принятой миграции данных, passing fault tests или исправленного prod.

## Читать по порядку

1. AGENTS.md, README.md, docs/index.md, docs/principles.md, docs/boundaries.md.
2. [Исходные факты и пределы аудита](research.md).
3. [ADR-0087](../../../docs/adr/ADR-0087-broker-bounded-sqlcipher-store.md) —
   Broker SQLCipher и recovery.
4. [ADR-0088](../../../docs/adr/ADR-0088-bounded-control-journals.md) —
   существующие root-plane files/receipts.
5. [SPEC-0087](../../../specs/active/SPEC-0087-broker-bounded-storage.md) и
   [SPEC-0088](../../../specs/active/SPEC-0088-bounded-control-journals.md).
6. [Acceptance/fault matrix](acceptance.md), затем plan.md и state.yaml очередного CHG.

## Что именно решаем

| Класс | Владелец / итоговый substrate | Ограничение |
|---|---|---|
| Authorization state + safe audit Broker | SQLCipher в отдельном Broker module | Строгий current head, recovery и конечный hot/archive budget |
| Secret objects / connector snapshots | Существующие encrypted providers | Known ownership, current/pinned/retired, bounded pending delete |
| ToolHub state | Существующий JSON snapshot | Atomic sync, conflict fence, initialized-store refusal |
| Jobs/outbox/run outcomes | Существующий spool + canonical receipt | Replay производных записей, stable IDs, no blind retry |
| Subscription cursors / provider dedup | Раздельные typed directories | Active state не удаляется terminal-history compactor |
| Control audit | Существующий audit module, finite JSONL segments | Durable admission/completion, safe fields, protected unsettled |
| Diagnostics / trace | Ротируемые files | Bounded reads, exact scope, visible gaps |
| Hermes memory/artifacts/usage | Существующие владельцы | Без переноса на новую общую БД |

Обязательные свойства: Go trust boundary, model is untrusted, deny wins,
persist-before-claim, отдельные dev/prod, explicit migration, no secret DTOs.
Не включены root SQLite, PostgreSQL/TypeDB/DuckDB, workflow engine, vector store,
новые providers, agent mounts управляющей DB, silent patch upstream Hermes.

## Канонический порядок

Hard dependencies находятся в state.yaml. Этот порядок минимизирует повторное
проектирование; независимые root исправления можно продолжать при blocked
native gate, но зависимые этапы нельзя объявлять completed.

| Порядок | Изменение | Результат / зависимость |
|---|---|---|
| 1 | [CHG-0215: Надёжная текущая голова recovery брокера](../CHG-0215-broker-recovery-head/plan.md) | Можно начинать после rebaseline |
| 2 | [CHG-0216: Изоляция курсоров и безопасная очистка event records](../CHG-0216-event-state-isolation/plan.md) | Можно начинать после rebaseline |
| 3 | [CHG-0217: Ротация, курсоры и ограниченный trace диагностики](../CHG-0217-diagnostic-log-durability/plan.md) | Можно начинать после rebaseline |
| 4 | [CHG-0218: Gate сборки и аварийной атомарности SQLCipher](../CHG-0218-broker-sqlcipher-proof/plan.md) | После CHG-0215 |
| 5 | [CHG-0219: Таблицы состояния и постраничный audit Broker](../CHG-0219-broker-sqlcipher-store/plan.md) | После CHG-0215, CHG-0218 |
| 6 | [CHG-0220: Управляемые версии secret objects и безопасный GC](../CHG-0220-broker-object-lifecycle/plan.md) | После CHG-0219 |
| 7 | [CHG-0221: Ограниченный обязательный audit и честные исходы действий](../CHG-0221-bounded-control-audit/plan.md) | Можно начинать после rebaseline |
| 8 | [CHG-0222: Один receipt для результата, доставки и обработки source batch](../CHG-0222-spool-durable-handoff/plan.md) | После CHG-0216, CHG-0221 |
| 9 | [CHG-0223: Полный учёт объёма и безопасное операторское обслуживание](../CHG-0223-storage-budget-maintenance/plan.md) | После CHG-0217, CHG-0219, CHG-0220, CHG-0221, CHG-0222 |
| 10 | [CHG-0224: Сохранность ToolHub и доверенное подтверждение пользователя](../CHG-0224-toolhub-state-confirmation/plan.md) | После CHG-0221, CHG-0222 |
| 11 | [CHG-0225: Долговечная сверка проекций и понятные причины runtime-сбоев](../CHG-0225-runtime-revision-reconcile/plan.md) | После CHG-0217, CHG-0224 |
| 12 | [CHG-0226: Явная миграция и итоговая приёмка ограниченного хранения](../CHG-0226-storage-migration-acceptance/plan.md) | После CHG-0215, CHG-0216, CHG-0217, CHG-0218, CHG-0219, CHG-0220, CHG-0221, CHG-0222, CHG-0223, CHG-0224, CHG-0225 |

CHG-0218 — технический PASS/FAIL/UNAVAILABLE gate. Он выбирает exact native pins
и доказывает конкретный recovery protocol. CHG-0219 нельзя начинать по одной
документации SQLite. Gate failure не разрешает ослаблять durability или незаметно
сменить architecture; остаётся исправленный journal CHG-0215.
CHG-0226 — единственный финальный production-data migration/rollout этап;
предыдущие import/format tests используют disposable synthetic stores.

## Общие условия реализации

- На момент проектирования branch fix/m23-live-wake, HEAD указан в research.md,
  84 исходных изменённых paths, включая незавершённый CHG-0214.
  Перепроверить текущий tree. Не reset/stash/overwrite/commit чужую работу.
  Не переключать dirty checkout на dev по шаблону; изоляцию выбирать с владельцем
  текущего checkout, а не переносить незакоммиченный state автоматически.
- Прежде slice найти все callers; test red -> smallest fix -> green.
  Названия предложенных tests в plans не означают существования кода.
- Применять existing atomicJSON, syncSpoolDirectory, flock, streamReceipt и
  существующие finalizers; не создавать второй framework.
- Все previews/diagnostics используют allowlisted safe aggregates.
  Не читать auth files, envs или dictionary keys secret stores.
  Технические временные артефакты только под ignored .work/tmp/ после
  git check-ignore; synthetic test files — isolated t.TempDir/test volumes.
- Внешний send/login/publication не разрешены этим планом.
  Provider GC проверяется на локальных owned fixtures; live destructive apply
  требует отдельного конкретного operator instruction и текущей policy.
- Изменённый agent contract: description + prompt_contract + tools/routines
  docs + новая immutable catalog version, затем rebind и respawn.
- Не редактировать accepted ADR или hash-pinned SPEC-0064 задним числом.
  docs/ текущего поведения меняются после реализации; evidence остаётся в CHG.

## Что известно, а что определяется проверкой

Архитектурный выбор и hard invariants приняты. CHG-0218 должен получить
поддерживаемый exact binding/native build и результат failure matrix.
Это ограниченная engineering задача, а не приглашение заново исследовать 42 harnesses.

Численные bytes/files/reserve defaults выбираются в CHG-0223 по measured
footprint и worst-next-write, в пределах SPEC. Конечные limits обязательны,
глобальный unlimited default запрещён. Existing retention windows сохраняются.
Diagnostics default: 7 дней + finite bytes/files cap.
Security audit destructive retention не подразумевается: при отсутствии
явной policy export-only; eventual admission stop честно документируется.
План не обещает сохранить бесконечный audit на конечном диске.

## Gates и evidence

- Root: just check; own Go statement coverage >=85%.
- Broker: собственный just pipeline, >=85% overall и каждый production package.
  Из root: just credential-broker-check. Для отдельных команд использовать
  module justfile и рабочий каталог Broker; Windows-specific limitation записать.
- Dependency changes: just security плюс module security/release gates.
- Images: hubctl/just BuildKit paths; prebuilt gates по AGENTS.md, actual image
  changes требуют подходящего image-side container proof.
- No mock-as-live: logic, native filesystem, Docker, real provider — разные уровни.
  Отсутствующий gate остаётся unavailable/not_run, а не зелёным.
- Каждый finished CHG: actual evidence, docs reverified, move .work/done/,
  индекс в тот же день. GitHub milestone/issues пока не созданы; не выдумывать IDs.

## Проверка подготовки пакета (2026-10-28)

Проверки docs/links/numbering, whitespace и dependency DAG прошли; 12 state.yaml
разобраны настоящим YAML parser из существующей Go dependency, 36 slices
сопоставлены plans. Implementation всех CHG остаётся not_started.
Во всех states planning_just_check относится к этому общему planning run,
а just_check остаётся not_run для будущей реализации соответствующего CHG.

Полный just check запускался и завершился failed: существующий
internal/mediasvc TestComfyVideoResumesPersistedHandleWithoutSubmission получил
Windows temporary-file sharing violation; дополнительно упала запись coverage
metadata. Точечный race повтор этого теста прошёл (2.082s), но это не превращает
весь gate в passing. Итоговый aggregate coverage не получен.
Production code не исправлялся в рамках подготовки документов. В следующей
сессии сначала перепроверить baseline; native/recovery/live acceptance пока нет.

## Промпт для следующей сессии

Реализуй Storage reliability MVP в этом репозитории по данному пакету.
Сначала прочитай AGENTS.md, README.md, docs/index.md и этот README целиком,
затем ADR-0087/0085, SPEC-0087/0088, research.md и acceptance.md.
Перепроверь dirty tree и состояния CHG-0215..0202; сохрани чужую работу,
особенно CHG-0214. Начинай с CHG-0215 и иди по таблице порядка, соблюдая
hard dependencies из state.yaml. В каждом slice сначала воспроизведи
регрессию тестом, затем внеси минимальное исправление и обнови evidence/state.
SQLCipher вводится только внутри Broker и только после PASS CHG-0218.
ToolHub и Communication сохраняют текущие JSON/spool substrates.
Сначала synthetic fixtures/isolated volumes; live migration только по
CHG-0226 с verified backup и operator policy. Не понижай authorization,
audit, recovery, confirmation и isolation ради прохождения проверки.
Выполняй AGENTS gates; не называй mocks живой интеграцией. Не делай login,
external send, publication или destructive provider cleanup без прямой
инструкции. Доводи готовые CHG до фактической приёмки, обновляй current docs
и перемещай в done; если native gate не проходит, честно заблокируй только
зависимые этапы и продолжи независимые исправления без новой архитектуры.
