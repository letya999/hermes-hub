# M32: наблюдаемые задачи вместо одного большого cutover

Дата: 2026-11-05. Текущий маршрут выполнения; заменяет очередь из старого
handoff, но не ADR-0095/0096 и SPEC-0094/0095. Исторические аудиты сохраняются.
Текущие назначения и зависимости: [GitHub/worktree runbook](parallel-work.md).
Нумерация 22 внутренних срезов ниже заменена 20 GitHub issues; это справка
по техническим сценариям, не отдельная очередь или status tracker. CHG остаются владельцами
работы; новые ADR, сервисы, трекеры и дублирующие CHG не нужны.

## Что изменилось после follow-up аудита

Проверено по текущему исходному коду, HEAD `97f79b68`, 225 изменённых/новых путей
до этой декомпозиции. Количество файлов не является мерой готовности.

| Область | Текущее доказательство | Остаток |
|---|---|---|
| Deployment contract | Реальные CompileAuthored/Publish, dev/prod, удалён legacy axis | Сохранять этот путь в новых проверках |
| Managed config | Browser/timeouts проверяются в managed; ImageGen уже перенесён | self-settings и конечная карта остальных авторов |
| Hosting | Plan адресует hosting/controller; reset удаляет старый controller config | Полная приёмка на актуальном образе |
| Rollback | Восстанавливает конкретный profile; пересекает права с текущими выданными; replay сервисных действий | Сбой процесса, смена binding A→B, согласованность всех читателей |
| Tools | Встроенные tools выводятся из паспорта; scopes сейчас явно отклоняются | Scoped admission и connector/raw-MCP selection |
| Organization | settings.yaml — защищённая host policy; scope mtime больше не выбирает policy | Классификация источников в A06 |
| Memory | Mount isolation и memory.enabled gate присутствуют | Реальный backend, exact run, capture/recall/edits |
| Automation/dreaming | План есть, движок остаётся в communication | R01–R04 и D01–D03 |

В CHG-0283 записаны `just check` exit 0, coverage 86.00% и прежний Docker gate
с локальными fixtures. Это свидетельства предыдущего исполнителя; Docker gate
устарел после последующих изменений. В этом разборе отдельно прошли пять целевых
тестов migration/reconcile/toolhub (см. state.yaml). Верхний state и ограничения
в дочерних state противоречили более свежим evidence; они синхронизированы.

## Почему возникло буксование

1. Срезы объединяют хранилище, авторизацию, транспорт, mount и продуктовый сценарий.
   «Перенести память» не имеет маленькой проверяемой точки завершения.
2. Зависимость всей памяти от переноса всех автоматизаций искусственна: поиск
   не требует scheduler. Реальная зависимость появляется у dreaming.
3. Старый handoff снова поручает уже закрытые ошибки. Исторические записи тестов
   смешаны с текущим статусом, что провоцирует повторные широкие проверки.
4. Сначала меняются схемы/mount, а рабочий вызов откладывается до последнего этапа.
   Удаление mount не доказывает ни работоспособность, ни изоляцию памяти.

## Контракты реализации

- Единственный автор конфигурации — шесть паспортов. Редактирование draft не
  меняет выполняемую конфигурацию до проверенной публикации. Секреты, защищённые
  grants, connection state и generated state имеют другую роль и сохраняются.
- ToolHub получает identity из проверенной аутентификации. Tool arguments,
  произвольный user/path и один только request run_id не задают полномочия.
- Для memory вызова нужен exact-run контекст: principal, environment, job/run,
  audience, lifecycle и текущая policy. Связь с проверенным запуском supervisor
  подтверждается существующим доверенным control channel; correlation сама по
  себе не является подтверждением. Background/system runs имеют явный тип.
- ToolHub использует существующие vault/search/night/routines packages и storage.
  Новый singleton service, generic bus, очередь или база не вводятся.
- Communication подтверждает транспортные факты и доставляет. Дедупликация
  accepted-turn/completed-run происходит по стабильному producer/event ID с
  проверкой владельца; receipt сохраняется до transport ack. Completed-run —
  исторический факт для capture, а не разрешение на новый model-tool вызов.
- Перенос каждого writer/clock имеет одну точку включения и одновременно
  отключает старый writer/clock. Не держать совместимость или двойное исполнение.
  Старые пользовательские данные не удалять и не конвертировать автоматически.
- Публикация через несколько процессов не становится транзакцией от mutex CLI.
  Для A04/A05 спроектировать один revision/activation marker и durable progress
  record: читатель использует согласованный снимок либо отказывает; незавершённое
  изменение не получает статус applied. Recovery использует существующий store,
  а не распределённый transaction framework. Запись прогресса — operational state.
- Подтверждение memory edit привязано к user, target, exact diff, base, expiry,
  single-use ID. Применение diff и погашение подтверждения восстанавливаются как
  одна операция. Replay после crash не должен повторно менять память.
- Dreaming key: environment + principal + local date + routine identity.
  Не включать revision настроек в уникальность дня: изменение config не должно
  запускать второй commit в тот же день. Retry вычисления допустим, двойной commit
  запрещён. После timezone change не повторять уже завершённый local-day key;
  DST overlap даёт одну occurrence, DST gap — первый допустимый момент после времени.
  Эти правила — проект приёмочных случаев в рамках once-per-local-day контракта.

## Правило одной задачи

Одновременно выполняется одна задача. Не запускай другие чаты/агентов без
отдельного указания владельца. Не бери следующий глобальный «довести M32».
Задача = одно наблюдаемое изменение + нужные callers/guards + регрессия.
Если сценарий не помещается в один проверяемый changeset, раздели задачу до кода,
сохранив отрицательные проверки безопасности в том же changeset.

Статусы: pending → active → verified; blocked содержит конкретную причину.
`verified` требует реализации, named test, exit code и наблюдаемого эффекта.
Один тест на fixture подтверждает только названную границу, не весь этап.
CHG становится done только после его полной приёмки; verified карточка не равна done CHG.

Receipt задачи в GitHub issue/PR и соответствующем CHG evidence: кодовая ревизия либо digest затронутых исходников
при dirty tree, команда, exit code, наблюдаемый результат, непроверенные границы.
Не сохранять содержимое памяти, токены, полные env или реальные user IDs.
Raw output — только ignored .work/tmp после git check-ignore.

На каждую задачу: red regression → минимальный код → scoped suite → требуемый
`just check` перед сдачей по AGENTS.md. Тяжёлый Docker gate — по изменённой границе
и правилам AGENTS, не заново для каждой правки текста. Не отменять обязательные
gates и не повторять неизменённый gate без причины. Изменения tool contract
всегда включают catalog, prompt_contract, docs/tools и docs/routines вместе.

## Очередь A: оставшиеся конфиги и activation (CHG-0283)

### A01 — settings edit публикует паспорт
- Anchors: agenttools/settings.go:SettingsSet, stack/hermesconfig.go:applySelfSettings,
  supervisor/env.go, runtime/toolhub_config.go, cmd/hubctl/space_apply.go.
- Результат: поддержанные setting edits меняют config через publisher; overlay
  больше не читается и не записывается. Прямое редактирование draft не активирует его.
- ADD `TestSettingsEditPublishesAuthorizedConfig`: с правом появляется новая revision,
  restart сохраняет значение; без права revision прежняя; poisoned overlay не влияет.

### A02 — scope правил tools доходит до admission
- Anchors: migration/rights.go:selectedHubTools; toolhub/capability_store.go,
  gateway_call.go; passport/compile_v2.go.
- Результат: разрешён docs/, запрещён private/ при одинаковом выбранном инструменте
  и выданных правах; narrowing работает в открытой сессии до backend dispatch.
- ADD `TestPublishedToolScopeConstrainsCall`: out-of-scope backend не вызван;
  неизвестное/неподдержанное правило отклоняется до изменения current/profile.

### A03 — connector selection также принадлежит tools.yaml
- Anchors: migration/rights.go; toolhub/rights_ledger.go:deriveRights;
  toolhub/capability_store.go; passport/compile_v2.go:checkConnectors.
- Результат: удалить raw-MCP/connector из паспорта — удалить из list и запретить
  call уже открытой сессии. Connection/credential state остаётся, grants не меняются.
- ADD `TestPassportRemovalRevokesConnectorSelection`: локальный synthetic MCP,
  два connection IDs; удалён только нужный инструмент; повторный apply идемпотентен.

### A04 — читатели видят одну revision
- Anchors: passport/publish.go, passportview/view.go, stack/materialize.go,
  toolhub admission/config-view reader; inventory всех читателей current/view.
- Результат: pinned immutable bundle и activation marker читаются согласованно;
  в промежуточном состоянии читатель отказывает, а не смешивает grants/config/tools.
- ADD `TestConcurrentReadersNeverMixRevisions`: управляемая пауза на каждом publish
  шаге; читатели видят полный old/new snapshot или явный отказ. Без timing sleeps.

### A05 — recovery после остановки apply
- Anchors: space_apply.go:applyConfirm, migration/rights.go, reconcile.Run.
- Результат: durable phase/affected set восстанавливает все затронутые профили и
  сервисы либо сохраняет явное denied/diverged. Включить смену binding A→B.
- ADD `TestApplyCrashRecoveryPreservesRevocation`: fault injection после publish,
  profile update, первого service effect; restart, revoke, повтор recovery;
  соседний dev/prod не меняется, отказанный B не остаётся активным. Зависит A04.

### A06 — закрыть карту источников конфигурации
- Anchors: stack.Settings, execution selection, connector settings, selfsettings,
  scope/org readers; compiler/consumer registry; docs/spaces.md.
- Результат: конечная таблица old field → passport/consumer или protected state
  или removed; для каждого оставшегося writer указан владелец и право.
- UPDATE имеющиеся authority tests: poisoned retired input не меняет materialized
  config. Никаких новых широких regex-тестов вместо проверки поведения.

## Очередь M: память, без зависимости от готового scheduler

### M01 — ToolHub разрешает vault по principal (CHG-0285; первая задача)
- Anchors: toolhub/server.go:EndpointConfigFromEnv/NewEndpointHandler,
  stack/compose.go:HUB_VAULTS_BY_PRINCIPAL, internal/vault.
- Результат: production reader bindings подключён к backend; два synthetic vault,
  ограниченные пути и отдельные user locks. Никакого model-supplied root.
- ADD `TestToolHubVaultResolverIsolatesUsers`: A/B содержат разные маркеры;
  неизвестный principal/env, traversal и symlink отказывают; блокировка A не держит B.
- Предел: это backend boundary, ещё не работающий model tool. Runtime mount не возвращать.

### M02 — проверенный exact-run контекст (CHG-0285)
- Anchors: toolhub/gateway_call.go, api.CallCorrelation, supervisor execution/control,
  communication/core.go и memory_capture.go; existing authenticated service channel.
- Результат: ToolHub отличает одновременно идущие private/group/background runs,
  начало/конец/restart; lookup опирается на проверенный запуск, не строку из arguments.
- ADD `TestMemoryRunCannotBorrowOwnerAuthority`: private A и group A параллельно,
  чужой run ID, finished run и wrong env запрещены даже при живом owner run.
- Старый shared stamp больше не используется новым memory execution path.

### M03 — первый рабочий memory_search через ToolHub (CHG-0286)
- Anchors: agenttools/memory_search.go, toolhub/server.go routing,
  gateway_call.go, internal/memory/search. Зависит M01/M02.
- Результат: реальный tools/call возвращает marker только своего vault при runtime
  без mount. Контракт и backend переключаются вместе, старый executor удаляется.
- ADD `TestMemorySearchRoundTripIsolated`: two-user реальный HTTP handler/storage,
  selected tool + memory=false, tools.use revoke и memory.search revoke отказывают;
  backend spy только дополняет реальный результат, не заменяет его.

### M04 — durable facts и capture в ToolHub (CHG-0285)
- Anchors: communication/spool.go/memory_capture.go, toolhub/server.go,
  internal/memory capture/writer packages. Зависит M01/M02.
- Результат: persist-before-ack, trusted event → один capture; отключён старый
  communication writer, retry остаётся в spool при недоступности ToolHub.
- ADD `TestCaptureRetryAfterRestartCommitsOnce`: потерян ack после commit,
  restart обоих концов, duplicate/out-of-order/foreign event; одна запись,
  правильные provenance/audience; revoked memory до commit даёт ноль записей.

### M05 — recall/CORE без прямого vault (CHG-0285)
- Anchors: communication/core.go/recall paths, agenttools/context paths,
  ToolHub memory backend. Зависит M03/M04.
- Результат: prompt получает разрешённую память через exact-run сервисный вызов;
  communication больше не читает vault, shared run-scope/current удалён со всеми callers.
- ADD `TestRecallDoesNotLeakAcrossConcurrentRuns`: разные private/group маркеры
  в результирующем prompt; выключение памяти убирает recall без fallback на файлы.

### M06 — remember через единственного writer (CHG-0286)
- Anchors: agenttools/memory_card.go, toolhub memory routing, internal/vault.
  Зависит M03/M04.
- Результат: создание новой карточки требует remember; изменение/merge/forget
  существующей не проходит под remember. Повтор call ID не создаёт второй объект.
- ADD `TestRememberRechecksGrantAtCommit`: barrier перед commit, revoke/disable,
  отсутствие записи; retry разрешённой операции даёт одну карточку своего пользователя.

### M07 — proposal/confirm/apply для edit (CHG-0286)
- Anchors: ToolHub confirmation store, communication/approval.go как транспорт,
  vault writer. Зависит M06; это один неделимый security changeset.
- Результат: предложение → проверенное owner action → CAS по base → single-use
  commit. Изменение существующей памяти без этой цепочки недоступно.
- ADD `TestMemoryEditAppliesExactConfirmedDiffOnce`: модельный confirm, другой user,
  expired/stale base/revoke запрещены; crash между commit и receipt + retry не дублирует.

### M08 — OpenKnowledge через governed path (CHG-0286)
- Anchors: stack/compose.go и knowledge integration, ToolHub backend. Зависит M05/M07.
- Результат: убрать прямой RW vault; чтение/изменение проходят те же границы;
  несовместимый mutation endpoint явно unavailable до его адаптации, не обход.
- ADD `TestKnowledgeMutationUsesConfirmedWriter`: container mount inspection и
  фактическая правка synthetic card с подтверждением; прямой write невозможен.

## Очередь R: существующий routines engine в ToolHub (CHG-0284)

### R01 — routine CRUD и state ownership
- Anchors: routines/engine.go:NewEngine, communication/spool.go/http.go,
  agenttools/routine.go, toolhub/server.go.
- Результат: создать/прочитать/pause/delete через ToolHub с principal/env;
  один writer и lock; старый communication CRUD writer отключён в том же changeset.
- ADD `TestRoutineCRUDSurvivesCommunicationRestart`: реальное storage, A/B isolation,
  revoke до mutation; restart gateway не меняет schedule. Не запускать второй clock.

### R02 — assignments/watches и переходы состояния
- Anchors: communication/assignments.go, watch handlers, routines engine и tool routing.
  Зависит R01.
- Результат: состояния и mutations принадлежат ToolHub, существующие verbs сохранены;
  acceptance через public path, не только перенос файлов между packages.
- ADD `TestAssignmentWatchTransitionsOwnedByToolHub`: create/disable/resume/retry,
  wrong user deny; communication stop не теряет состояние.

### R03 — occurrence clock и supervisor dispatch
- Anchors: communication/occurrences.go/gateway.go, routines/engine.go, ToolHub lifecycle.
  Зависит R02/M02.
- Результат: clock только в ToolHub; stable occurrence ID и durable claim;
  supervisor idempotency подавляет повтор эффекта после lost ack/restart.
- ADD `TestDueOccurrenceDispatchRecoversOnce`: fake controllable clock, два worker
  attempts, crash после dispatch; один accepted job при выключенном communication.

### R04 — результат и доставка независимы
- Anchors: ToolHub dispatch completion, communication/gateway_delivery.go/outbox.
  Зависит R03.
- Результат: run result хранится независимо от доставки; ToolHub повторяет pending
  delivery request до durable ack; gateway хранит channel receipt. Два разных владельца
  состояния, не два scheduler и не два writer одного delivery receipt.
- ADD `TestCompletedRoutineWaitsForDeliveryOnly`: gateway down после run; восстановить,
  один digest через idempotency key. Не обещать external exactly-once при отсутствии
  такого контракта у канала; ambiguous delivery обрабатывается существующей политикой.

## Очередь D: dreaming (CHG-0287)

### D01 — защищённая daily routine
- Anchors: routines engine, ToolHub lifecycle, config.memory fields. Зависит R03/M04.
- Результат: system routine из опубликованного config, user CRUD не может её менять;
  каждому principal/environment свой local-day key.
- ADD `TestDreamingLocalDayAndProtection`: два timezone, DST gap/overlap,
  изменение config в том же дне; максимум одна завершённая occurrence на day key.

### D02 — night execution и crash-safe commit
- Anchors: internal/memory/night, communication/nightjob.go, ToolHub writer.
  Зависит D01/M06.
- Результат: только ToolHub запускает night; per-user relay/vault; durable checkpoint,
  memory.enabled/dreaming grant при admission и перед commit, старый night worker удалён.
- ADD `TestDreamingRestartCannotDoubleCommit`: crash до/после commit, retry,
  revoke на barrier; один commit либо ноль при revoke, другой user независим.

### D03 — digest и предложения действий
- Anchors: ToolHub night completion, delivery R04, assignments R02. Зависит D02/R04.
- Результат: digest доставляется после outage; предложенные future assignments
  остаются drafts до owner approval, dreaming не активирует их самостоятельно.
- ADD `TestDreamingOutageKeepsDraftsUnscheduled`: restart + gateway down/up,
  один локальный delivery request и ни одного автоматически запущенного proposal.

## Z01 — итоговая приёмка (CHG-0288)

Зависит от всех предыдущих задач. Два synthetic пользователя, реальный pinned
runtime/ToolHub/supervisor/storage, local model/channel fixtures; реальные accounts
не требуются. Проверить search/remember/edit/recall, overlapping runs, grant revoke,
restart, dreaming и отключение communication. Проверить отсутствие старых config,
vault и scheduler authorities; catalog rebind/respawn и loaded contract version.
Обновить docs, выполнить актуальные just check/coverage/Docker gates; отдельно
назвать границы с local fixtures и live providers. Только после этого закрывать CHG.

## Прежняя последовательная очередь (заменена GitHub назначениями)

Актуальный запуск: foundation, затем #508/#513/#521 в отдельных worktree.
Использовать [parallel-work.md](parallel-work.md) и приложенные промпты.

Выполни только M01. Прочитай её anchors и callers, выбери существующие vault
resolver/storage primitives, добавь реальные negative isolation tests и production
binding reader. Не переноси scheduler и не запускай новый раунд всего M32.
Обнови receipt M01; объясни, что model tool заработает после M02/M03.
Следующие назначения: M02, M03. Это первая наблюдаемая продуктовая точка:
два пользователя выполняют memory_search через ToolHub без runtime vault mount.
Затем продолжай по зависимостям, чередуя готовые A/R задачи с memory задачами.
