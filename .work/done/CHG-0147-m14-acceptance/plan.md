# CHG-0147: Milestone 14 acceptance and connector lifecycle repair

Chain: ADR-0053 through ADR-0059, SPEC-0060 through SPEC-0063, and issues #281–#289, #104, #105 and #133. This record tracks the remaining acceptance after the original implementation CHGs were moved to done.

## Goal

Verify the milestone against the running local stack and two real team principals, repair concrete failures, and close only issues whose acceptance has evidence. Public HTTPS and remote-device acceptance stays in #283 outside this local-dev milestone.

## Slices

1. Verify installed GitHub, Workspace, Calendar, Playwright and other MCPs from current store, audit and Docker labels. No new provider consent is inferred from a green unit test.
2. Wire project- and connection-scoped Docker label query/purge into the served ToolHub endpoint. Test cross-project and connection-reuse denial before deployment.
3. Prove restart-safe finalizer resume, periodic orphan/credential/surface reconciliation and zero-resource receipts on a disposable stateful connector.
4. Exercise operation journal and source ledger order, Broker isolation/rotation, CI image provenance and install budgets with the relevant test and live gates.
5. Prove team connection lifecycle with the two owner-authorized Telegram principals, including distinct Broker grants and revoke behavior. Use an operator-reviewed shareable definition and a dedicated test credential or an explicitly chosen service.
6. Prove browser viewer login and profile purge; re-check natural-language disable/remove and OAuth isolation. Close each issue only when its listed criteria pass.

## Safety and gates

## October 22 audit completion

The six open issues are #104, #286, #287, #288, #289 and #293. Preserve the
existing CHG-0148 working changes. Implement and verify in this order:

1. Broker-aware reconciler liveness and purge; HTTP regressions distinguish active,
   revoked/missing and unavailable Broker credentials without local-store fallback.
2. Truthful connector status and install/remove generations; replay, owner isolation
   and concurrent requests retain idempotency within one installation.
3. Credential delivery consumption, tmpfs materialization and cross-process refresh.
4. Team Broker grants, operator finalizers and persisted sender attribution.
5. Functional authenticated browser viewer and supported portable backend recipes.
6. Catalog image/prefetch/import and performance gates; general auth-first onboarding.
7. Full hub and Broker checks, security and Docker gates, then separately recorded
   live acceptance. Account login, external sending and publication require the
   owner's concrete instruction; unavailable evidence never counts as passed.

- No secrets, links, provider data or personal logs enter this record.
- Fail closed on ambiguous ownership. Never mutate unrelated connections during acceptance.
- Run focused red/green tests, `just check`, then the applicable local Docker gate; record any unavailable CI or public-device gate honestly.

## Остановка по запросу владельца — 2026-10-22

Разработка остановлена. Изменения остаются в рабочем дереве ветки
`feat/m14-toolhub-connector-platform-merge`; коммитов и push этой работы нет.
Предшествующие изменения CHG-0148 сохранены. Последние правки не развёрнуты.
Номер milestone в GitHub — 14, название — **M13 — ToolHub Connector Platform**.
Открыты #104, #286, #287, #288, #289 и #293. Ни один из них не выполнен целиком
по своим критериям, поэтому на этой остановке закрывать нечего.

### Что реализовано и проверено по частям

- Reconciler читает состояние credential из Broker и удаляет его через Broker;
  отсутствие локального ciphertext больше не означает потерю Broker credential.
  HTTP-регрессии различают active, revoked, 404 и недоступность сервиса.
- Install/remove имеют поколения после удаления; восемь конкурентных повторных
  установок сходятся к одной новой операции. Старые записи сохраняются как
  tombstones. Тест повторного удаления использует новое допущенное onboarding,
  поэтому это ещё не доказательство всего живого install/remove/install.
- `connector_status` отражает degraded-соединение как требующее действия.
- Refresh lease получил файловую блокировку и перечитывание журнала. Проверены
  50 независимых Store, сохранение чужого lease и наблюдение release; отдельные
  OS-процессы и реальный обмен rotating refresh token ещё не проверены.
- Реальный JSONL audit сохраняет sender_id, team_id и binding_id; неверные
  значения этих полей отклоняются.
- Общий auth-first рецепт сохраняет несобранный draft, открывает Broker-форму и
  возобновляет review/build/tools-list после авторизации. Проверены передача
  readonly Broker file mount, сохранение контракта и метаданных, отдельные
  form_ready/artifact_ready/tools_confirmed. Полный OAuth-first пока не готов.
- Credentialed stdio и HTTP/SSE probes используют внутреннюю сеть и managed
  proxy. Проверены опасные mount paths, symlink-компоненты и CSV-инъекция через
  запятую в target. Обычный crash не превращается автоматически в auth-first.
- Readiness удаляет env-файл после admission; cleanup удаляет file credentials
  после успеха и ошибки записи env. Материализация только в tmpfs ещё не доказана.
- PrefetchPlan больше не учитывает одно удаление/один digest несколько раз.
  Подключения планировщика к runtime пока нет.
- Go обновлён до 1.27.3; совместимые анализаторы закреплены. В Broker SyncDir
  отклоняет symlink/обычный файл, HTTP-диагностика allowlist-ит метод и маршрут.

### Проверки на момент остановки

| Проверка | Фактический результат |
|---|---|
| Broker release-check | Пройден: race, docs/OpenAPI, build, fuzz, staticcheck, gosec, govulncheck; coverage 90,48%, все production packages выше 85% |
| just security | Пройден для запуска после обновления Go: нет затронутых Go symbols, npm audit 0 |
| Focused race для audit/auth-first/lease/lifecycle | Пройден; последние cleanup-правки внесены позже и требуют повторного прогона |
| Docker TestPreparedProbeProxyUsesInternalNetwork | Пройден на настоящем Docker |
| just docker-check | Пройден на предыдущем снимке исходников; финальные auth-first/cleanup/Broker-правки требуют нового image gate |
| devcheck docs | Последний отдельный запуск пройден до записи этого checkpoint |
| just check | Последний полный запуск упал на internal/devcheck TestDockerExecWrappers; финального полного покрытия Hub нет |
| Root gosec | 200 результатов требуют разбора; это не 200 подтверждённых уязвимостей. Безопасный отчёт: ignored .work/tmp/gosec-summary.json |
| Последний red-first regression | TestWriteWorkloadCredentialFileCannotFollowSymlink падает; исправление ещё не внесено |

### План завершения — в порядке зависимостей

1. **Восстановить безопасную и проверяемую рабочую базу.** Исправить запись
   `WriteWorkloadCredentialFile` через существующий symlink: атомарная безопасная
   запись с сохранением 0600, проверка повторной записи и отсутствия изменений
   вне workload. Проверить ошибки cleanup. Разобрать TestDockerExecWrappers,
   результаты root gosec и шум line endings в scripts/quality.py. Не отключать
   проверки целиком ради зелёного результата. Получить свежие `just check`,
   >=85% own statement coverage и race для затронутых границ.

2. **#287 — завершить границу credentials.** Подключить потребление ProxyGrant
   на реальном MCP-пути; доказать подстановку Authorization на fixture API и
   только stub в inspect/environ/filesystem. Перенести все временные env/file/
   proxy-rule материалы в проверяемый tmpfs, сохранив долговечность профилей и
   рабочих данных. Удалить параллельный connector-путь к local credstore после
   проверки миграции и rollback. Проверить 50 параллельных refresh-вызовов через
   несколько процессов, вращение refresh token, revoke/logout во время обмена
   и reauth при смене provider/client/endpoint. Затем живые GitHub, Notion,
   Calendar file+OAuth и один remote OAuth MCP.

3. **#293 — закончить общий auth-first.** Реализовать writable OAuth state и
   checkpoint после остановки writer; восстановление после restart. Проверить
   неверные credentials, cancel/expired link/network failure, конкурентные
   попытки разных владельцев и повтор без пересборки неизменного артефакта.
   Классифицировать неподдерживаемые интерактивные серверы явно. Проверить
   автоматический и уточнённый рецепты, отсутствие секретов в build/argv/logs,
   оба API и инструкции чат-агента. Закрывать только после воспроизводимого
   живого прогона совместимого MCP.

4. **#104 — полная приёмка lifecycle.** На свежем dev image провести обычные
   Telegram-команды disable → исчезновение tools/отказ открытого call → enable
   без повторного ввода credentials → remove с подтверждением → verified-zero
   → отказ старых credential/OAuth ссылок → новая установка того же GitHub
   source. Сохранить свидетельства без provider data и секретов; Hermes и
   Communication Hub не перезапускать ради обновления проекции.

5. **#288 — довести команды до Broker.** Team-owned credential, отдельные
   member-bound grants, собственные audit identities. Операторские rotate,
   revoke/remove/allow-writes должны работать без личного onboarding оператора,
   remove — через общую лестницу finalizers. Удаление участника очищает только
   его workload/состояние. Проверить один workload для shareable:connection,
   два для shareable:credential, non-member denial и rotation in-flight.
   Завершить живым Notion-сценарием двух реальных principals.

6. **#289 — реализовать browser viewer.** Настоящий noVNC или CDP-screencast
   вместо HTML-заглушки; owner/operation-bound короткая сессия, one-time link,
   TTL и запрет чужого доступа. Переносимые pinned образы обоих поддерживаемых
   backend, read/write/eval toolsets, изоляция двух профилей, web egress с DNS
   rebinding negative test, сохранение профиля на disable и verified-zero на
   remove. Живой тест: вход человеком с другого устройства и чтение защищённой
   страницы агентом; агент не получает пароль из чата.

7. **#286 — собрать быстрый каталог.** CI build/publish T0 images в GHCR,
   проверенные digest/provenance/SBOM и nightly probes. Полное разрешение всех
   поддерживаемых import refs в recipe+ledger с подтверждением; подключение
   prefetch к запуску/смене каталога в пределах 8 GB и без чужих image prune.
   Проверить defaults/toolsets и warm start. В CI измерить p50/p95 и enforce:
   T0 <=15 s, T1 <=30 s, remove <=5 s, first call <=2 s. Завершить чистым
   стендом с Notion token, Linear remote OAuth и фоновым T2 install.

8. **Финальная поставка.** Обновить текущие docs/spec deltas и добавить новые
   решения, не переписывая принятые ADR/frozen specs. Повторить Hub/Broker gates,
   `just security`, полный локальный Docker gate для финального кода. Выполнить
   штатный dev-rebuild, проверить выбранный dev image и endpoints. Для каждого
   issue приложить соответствие всех acceptance criteria конкретным CI/live
   свидетельствам; закрывать по одному, milestone — после последнего.

Вход в аккаунты, внешняя отправка и публикация требуют конкретной инструкции
владельца. Настройка реальных team principals/capabilities требует проверки
оператором. Дальнейшая разработка возобновляется только по новому запросу.
