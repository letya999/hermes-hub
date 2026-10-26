# Haloy: source review для M18 #147

Дата: 2026-10-26. Проверен clone `C:/Users/User/a_projects/haloy`,
commit `3cd1d500394a81532970dc4cdb57e02d77b2be62`, tag
`v0.1.0-beta.73`. Все ссылки ниже закреплены на этом commit.
Это design evidence, не интеграционный приёмочный тест.

## Решение

**Целиком Haloy не берём. Ограниченный HTTP-адаптер поверх неизменённого
Haloy тоже не выбираем. Fork его controller/proxy для MVP не оправдан.**
Расширяем существующий Go control plane и ToolHub admission; пишем только
недостающий hosting contract: slots/releases, share gate, quotas, leases и
reconciliation. Sandbox не пишем: используем готовый runsc либо проверенный
Kata через существующую admission boundary. sish/DNS остаются из #145/#146.

Из Haloy берём проверенные архитектурные приёмы, перечисленные ниже.
Копировать его код сейчас не предлагается: наиболее независимый healthcheck
потребует изменения семантики, остальные небольшие части дешевле выразить
в нашей существующей модели, чем поддерживать второй deployment stack.
Это вывод по границам контрактов, а не измерение календарной стоимости.

Haloy — содержательная готовая Go PaaS для **доверенного оператора**:
image deploy, replicas, readiness, rolling replacement, logs, TLS и rollback.
Отсутствие нашей tenant/security policy не является само по себе уязвимостью
в его заявленной модели. Но agent-generated workloads в Hermes требуют
другого набора обязательных гарантий до первого запуска.

## Что изучено

Прочитаны CLI/config/secret resolution, API/auth, container/volume/image
operations, deployment/history, discovery/updater/events, healthcheck,
certificates, proxy/router/transports, control socket/snapshot/reconcile,
logging/storage, тесты этих границ и CI. Не каждый из 136 production-файлов
проанализирован одинаково построчно; глубина сосредоточена на путях,
которые определяют совместимость с SPEC-0079.

Инвентаризация: 136 production Go files, 22 429 физических строк вместе с
комментариями/пустыми строками; 69 test files, 15 755 физических строк.
Это не executable LOC и не оценка необходимого нам объёма.
[go.mod](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/go.mod)
содержит Go 1.26.6, Docker client v28.0.4 и modernc SQLite; внешние
Postgres/Redis не обязательны. Это существенно легче крупных PaaS.

Есть три процесса: CLI `haloy`, controller `haloyd`, data plane `haloy-proxy`.
Reusable packages находятся под `internal/`: обычный import из модуля hub
запрещён правилами Go. Для reuse нужен отдельный upstream public package,
fork или extraction, а не просто `go get`.
[MIT license](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/LICENSE)
допускает extraction с сохранением copyright/license notice; оригинальный
код Hermes остаётся AGPL-3.0-only.

## Сопоставление с requirements

| SPEC-0079 | Что даёт Haloy | Что остаётся у нас / конфликт |
|---|---|---|
| R1 identity | Один bearer token daemon-admin, app names | Trusted owner/env/zone, per-user namespace и cross-user authorization отсутствуют |
| R2 tools | Deploy/list/stop/logs/exec API и CLI | Name-only agent surface, strict manifest, idempotency/revision и безопасные DTO нужны отдельно |
| R3 release | Image build/push/deploy/history | Confined copy workspace → immutable static release, file isolation, revision transaction отсутствуют |
| R4 storage | Named volumes и bind mounts | Storage reservation, staging overlap, per-user hard bounds, owned volume checks нужны отдельно |
| R5 serving | Независимые daemon/proxy, Docker restart | Собственный TLS/router вместо backend за sish; custom private networks несовместимы с discovery без изменений |
| R6 access | Public host routing | Artifact bearer bootstrap, expiry/epoch/revoke, cookie/CSP boundary отсутствуют |
| R7 admission | Создание обычного Docker container | Secure runtime/profile/resource/inspect contract и Broker не реализованы |
| R8 lifecycle | Health selection, Docker events, persisted route snapshot | Durable desired revision/tombstone, TTL, bounded retries, revoke-first cleanup и fences нужны отдельно |
| R9 budgets | Реплики и несколько operational timeouts | Owner quotas, CPU/mem/PID, storage/rates/concurrency/bandwidth policy отсутствуют |
| R10 audit | Application/deployment/request logs | Durable allowlisted receipts и audit admission/backpressure — другой контракт |

Существование list/stop/readiness полезно, но не заменяет эти guarantees.
Нельзя суммировать строки таблицы как независимые небольшие adapters:
R7 меняет create/start, R5 discovery/relay, R8 updater/cleanup, R6 весь serve path.

## 1. API и владение

[middleware.go](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/api/middleware.go#L10)
проверяет единый bearer token с constant-time comparison.
[routes.go](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/api/routes.go)
защищает им административные deployment/exec/stop/registry/image/tunnel
операции. Это не tenant principal, не scoped site capability и не viewer auth.
Скрытый от агента token и namespacing в нашем адаптере могли бы закрыть
часть R1/R2; сами по себе эти отличия не запрещают adapter.

Но [handleDeploy](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/api/handlers_deploy.go#L15)
после validation запускает background goroutine и возвращает 202.
Нет durable operation admission с expected revision и tombstone fence.
Общий mutex updater сериализует discovery/cutover, а не все независимые
deploy/stop HTTP операции. Сериализация targets в одном CLI invocation
не запрещает второй CLI/API client.
[JSON decoder](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/api/json.go#L20)
делает `io.ReadAll` до decode: для deploy нет собственного MaxBytesReader.
У некоторых upload endpoints отдельные limits есть — это не утверждение
об отсутствии bounds на всех API.

Container selection по
[app-name label](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/docker/container.go#L514),
а не owner/env/project receipt. Trusted adapter может генерировать имена,
но нам всё равно нужны ownership checks для restore/stop/remove и disk state.
Это не утверждение, что обычный непривилегированный сайт может сам создать
Docker labels: для этого ему потребовался бы запрещённый доступ к daemon.

## 2. Dynamic envelope: главное препятствие adapter

[RunContainer](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/docker/container.go#L27)
перед ContainerStart задаёт Image/Labels/Env и HostConfig из NetworkMode,
`unless-stopped`, Binds. В этом пути нет обязательных Runtime, User,
ReadonlyRootfs, CapDrop, SecurityOpt/NNP/seccomp, CPU/memory/PID/tmpfs caps.
Не делается проверка полного inspect receipt до разрешения запуска/маршрута.
Проверка image platform compatibility — не такой receipt.

[TargetConfig](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/config/deploy_config.go)
не даёт adapter полей для этого envelope. User/image defaults или daemon
defaults могут частично ужесточить конкретный deployment, но не являются
проверенным обязательным per-site admission contract. Установка лимитов
после start оставляет окно выполнения без требуемых ограничений.

Профиль можно добавить fork-ом или внешним Docker authorization layer,
но это уже новая security integration и её отрицательные/реальные тесты.
У нас аналогичные admission/finalizer/inspect seams уже есть в ToolHub.
Повторять их вокруг Haloy ради его Docker create loop не уменьшает доверенную
часть системы. Отдельный daemon/VM на каждого пользователя также не заменяет
per-site бюджеты, site-to-site isolation и share/lifetime semantics.

## 3. Сети, mounts и уборка

RunContainer принимает custom network. Однако
[DiscoverContainers](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/haloyd/deployments.go#L79)
пропускает контейнер без сети `haloy`; readiness IP также берётся из
[constants.DockerNetwork](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/docker/container.go#L337).
Следовательно, «просто передадим отдельную private network» не сохраняет
штатный health/routing path. Подключить все сайты ещё и к общей сети значит
потерять предполагаемую сетевую изоляцию. Нужны переделка discovery/relay
или полностью внешний routing/lifecycle path.

[Volume parser](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/config/volume_spec.go)
поддерживает операторские binds/options, не confined source allowlist.
[EnsureVolumes](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/docker/volume.go#L17)
переиспользует существующий named volume без проверки соответствия его
ownership label. Quota/reservation и immutable static publish отсутствуют.

[PruneImages](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/docker/image.go#L231)
использует `ImagesPrune(filters.Args{})`: daemon-wide dangling-image cleanup,
а не project-scoped selection. Он вызывается и после deployment/history,
и из maintenance. Это не broad volume prune и не удаление используемых
образов, но всё равно не соответствует запрету затрагивать другой проект.

## 4. sish, public serving и статика

[haloy-proxy startup](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/haloyproxy/haloyproxy.go#L78)
передаёт `:80`, `:443`.
[httpHandler](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/proxy/proxy.go#L391)
обычные app requests перенаправляет на HTTPS; исключения — ACME и localhost
API с проверкой loopback remote address. App routing находится в httpsHandler.
Поэтому sish TLS termination → обычный HTTP backend Haloy даёт redirect,
а не serve; простое подключение такого origin может замкнуть redirects.
Можно организовать второй TLS hop, namespace/port mapping или изменить proxy,
но это не готовая backend-конфигурация под наш контракт.

Haloy дополнительно управляет собственными per-host ACME HTTP-01 certs.
Нам уже дан wildcard TLS на sish, повторный cert manager не нужен.
Тот же proxy умеет направлять API-domain в privileged control backend;
bearer auth остаётся, но наш принцип — вообще не экспонировать control
listener через hosting data plane.

Обычные app hosts публичны: нет share epoch, fragment bootstrap, viewer
cookie gate, access revocation, per-owner serve budget/audit. Наш hosting gate
в любом случае нужен перед Haloy. Непосредственного workspace → immutable
static site slot тоже нет: статику можно упаковать в web-server image, но
это добавляет container на страницу и не решает immutable-copy/quota contract.

## 5. Lifecycle: сильные стороны и границы

Полезны initial discovery, восстановление Docker event stream,
debounce событий, readiness/min-ready-seconds, selection здорового deployment,
отдельный data plane и route snapshot. Это заметно больше, чем shell-wrapper
вокруг `docker run`. Docker `unless-stopped` сохраняет app после конца CLI.
Однако это не конечный lease, не bounded retry policy и не desired site state.

Исходники выявляют три случая, которые нельзя унаследовать как доказанную
гарантию безопасного cutover/recovery:

1. [Updater](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/haloyd/updater.go#L170)
   после `ErrUnreachable` при push продолжает обработку и позднее останавливает
   старые контейнеры. Snapshot сохранён для будущего reconcile, но proxy ещё
   может обслуживать старую таблицу. Подтверждение применения нового route
   не является обязательным условием остановки старого backend.
2. Cleanup исключает `app.deploymentID` из trigger event, а active deployment
   выбирается по лексикографически максимальному healthy ID. При запоздалом
   событии A после выбора B отсутствует проверка `trigger == selected revision`
   непосредственно перед stop/remove. Для достижения cleanup необходим
   changed deployment map либо initial path: без изменений есть early return.
   Возможная остановка более новой B — **source-inferred race scenario**,
   не воспроизведённый эксперимент и не заявленный upstream bugfix.
3. [Startup](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/haloyd/haloyd.go#L148)
   отправляет API-only snapshot до initial discovery. Старые app routes могут
   временно исчезнуть при restart controller до следующего успешного push.
   Наличие отдельного proxy не доказывает непрерывность на этом переходе.

Deploy history в SQLite — история config/images, не транзакционная очередь
desired revision/expiry/tombstone/finalizers. Stop также background 202.
Deploy vs unpublish с восстановлением после crash потребует наших durable
fences и согласования с уже запущенными внутренними операциями Haloy.
Внешний adapter не может отменить их одним удалением своего slot record.

## 6. Secrets, logs, HTTP

Local CLI hooks запускают shell с окружением оператора; secret resolvers
поддерживают env/1Password/SOPS. Это полезно для operator deploy, но не API
для untrusted agent manifest. [Deploy request/history](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/deploy/deploy.go)
передаёт resolved env в runtime и сохраняет raw rollback config JSON.
References задуманы unresolved, но literal secret env в raw config может
остаться в истории. Нельзя утверждать, что все resolved secrets обязательно
пишутся туда; риск касается переданных literals и отсутствия Broker contract.
Registry auth сохраняется локально в config с mode 0600, не через Broker.
Broker lease/materializer/revoke binding отсутствует.

[Request logging](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/proxy/proxy.go#L658)
пишет path, remote address, user-agent; failure diagnostics могут включать
app stdout и произвольные errors. Один error path secret resolver перечисляет
available dictionary keys. Это исходный код, реальные credential dictionaries
при исследовании не читались. Для hub нужно собственное allowlisted audit,
без app output/URL/body/credential keys и с bounded durable admission.

Proxy использует stdlib ReverseProxy, shared transports, X-Forwarded handling,
dial-only retry до отправки request bytes и tracking upgraded connections при
shutdown. Эти engineering детали полезны. Они не реализуют site auth,
Browser CSP/cookie policy, request budgets или безопасную обработку Broker env.
Не следует копировать весь proxy ради функций, уже имеющихся в Go stdlib.

## 7. Что именно можно заимствовать

| Часть | Готовность / зависимости | Решение для нашего MVP |
|---|---|---|
| `healthcheck` | 4 prod files, 614 физических строк; stdlib-only; 1248 test lines | Единственный компактный кандидат на extraction. Пока не копировать: нужен ли отдельный hysteresis monitor поверх admission/readiness — ещё не установлено |
| `proxywire` | 2 files, 196 строк + 184 test lines; wire schema/hash/file | Взять идею canonical hash + full snapshot; наша schema всё равно другая: owner/revision/epoch/expiry. Не вводить отдельный package ради нескольких primitives |
| `proxyclient` | 279 строк + 241 test lines; завязан на snapshot/socket | Взять reconcile по applied hash, retry и latest-intent guard. В одном hosting process нет необходимости вводить второй IPC service |
| `proxy` + `haloyproxy` | 1600 prod lines до остального dependency closure; config/TLS/API routing | Не переносить: убираем TLS/API/public routing и всё равно добавляем share gate; stdlib handler/proxy проще |
| `docker` + `deploy` + updater | Связаны с config, labels, image history, storage, networks, logging | Не переносить: критические seams уже есть в ToolHub, а отсутствующие guarantees находятся внутри этого кода |
| CLI/configloader/secrets | Build/deploy hooks, operator env, многоформатный config | Не переносить: агенту нужен узкий declarative контракт, secret flow принадлежит Broker |

Важная оговорка для `healthcheck`:
[StateTracker.SyncTargets](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/healthcheck/state.go#L44)
добавляет новый target **сразу healthy**. В Haloy это после предварительного
readiness, но нельзя использовать такую семантику как наш initial admission.
В monitor при нуле targets есть early return до SyncTargets; membership
и state transition имеют разные сигналы. Нужна проверка removal/reappearance
перед reuse. Даже этот пакет нельзя считать drop-in security component.

Конкретные patterns для реализации у нас:

- Control intent и serving state разделены; serve не зависит от жизни чата.
- Публикуется целый immutable routing snapshot, а не последовательность edits.
- Stable hash позволяет сверить desired/applied, periodic reconcile исправляет
  пропущенные события; Docker events — ускоритель, не единственный источник.
- Сначала readiness и подтверждённый revision cutover, затем drain/cleanup.
  Последовательность усилить относительно описанного Haloy failure path.
- Startup восстанавливает committed state, но всегда учитывает tombstones,
  expiry и ownership receipts; API-only snapshot не стирает last-known-good.

[WriteSnapshotFile](https://github.com/haloydev/haloy/blob/3cd1d500394a81532970dc4cdb57e02d77b2be62/internal/proxywire/file.go#L14)
использует temp+rename mode 0600. Это atomic visibility, но без file/dir fsync
не доказана power-loss durability. SchemaVersion — совместимость формата,
не монотонная site revision. Нельзя перенести оба предположения в наш store.

## 8. Сравнение вариантов внедрения

| Вариант | Что экономим | Что добавляем | Выбор |
|---|---|---|---|
| Haloy целиком | Готовая operator PaaS, TLS, build/image deploy, logs | Второй control/data plane, иная privilege model, отсутствующие hosting guarantees | Нет для #147; разумно для отдельного доверенного app-хоста при другой постановке |
| Unmodified Haloy + restricted adapter | Существующий Docker lifecycle | Наш весь hosting plane; нельзя выразить pre-start profile; private network discovery и lifecycle races остаются внутри backend | Нет: adapter не закрывает обязательный контракт |
| Fork Haloy | Можно изменить внутренние seams | Поддержка fork и upstream merges; меняем create/start, network/discovery, serve/TLS/auth, history/cleanup/logging | Нет для MVP; слишком много центральных subsystems ради сокращения уже существующего executor |
| Extract маленькие packages | Потенциально health hysteresis/snapshot utilities | MIT attribution, semantic adaptation, свои regression tests и ownership | Только при конкретной потребности; сейчас patterns, без code copy |
| Свой hosting layer + существующий ToolHub/sish/DNS + runsc | Переиспользование наших проверяемых trust boundaries | Недостающие slots/shares/quota/recovery | Рекомендуемый путь; не новая универсальная PaaS и не собственный sandbox |

Решение можно пересмотреть, если Haloy даст public embedding API с обязательным
pre-start policy/inspect hook, private-network discovery, external HTTP data
listener и durable operation/revision fencing. Это не требование к upstream
и не основание откладывать наш узкий static-first MVP.

## 9. Проверки и пределы доказательств

Docker/daemon/install scripts/hooks не запускались. Clone не изменён.
Запущены только unit/race tests и статический vulnerability scan:

- `go test -race -count=1 -timeout=3m ./internal/healthcheck ./internal/proxywire
  ./internal/proxyclient ./internal/proxy ./internal/haloyproxy`:
  четыре package pass; `proxy` падает в `TestCertManagerWildcardMatch` из-за
  недопустимого на Windows имени `*.example.com.pem`. Это platform fixture,
  не доказанный Linux production defect.
- Повтор только `proxy` с `-skip '^TestCertManagerWildcardMatch$'`: pass
  с race detector. Upstream тест не изменялся; единственный skip указан явно.
- `GOOS=linux CGO_ENABLED=0 govulncheck ./...`: exit 3, 12 symbol-reachable
  findings по scanner, из них 10 stdlib на **локальном Go 1.27.0** и два
  Docker module v28.0.4 (`GO-2026-4887`, `GO-2026-4883`, scanner fixed N/A).
  Это не утверждение о 12 эксплуатируемых server vulnerabilities или результате
  сборки на объявленном upstream Go 1.26.6. Stdlib findings зависят от toolchain;
  scan call traces включают init/client paths, а не доказательство эксплуатации.
  Локальный toolchain не обновлялся в этом проходе.
- Upstream `tools/vulncheck/main.go` допускает пять конкретных no-fix Docker
  advisory IDs, в том числе эти два, и отказывает при unexpected/fixed findings.
  `.github/workflows/test.yaml` помечает шаг vuln scan `continue-on-error: true`.
  Поэтому зелёный upstream CI не следует читать как отсутствие advisories.
- Taskfile `ci-test` = tests/vet/fmt; `test` запускает `go test -v ./...`,
  без race/coverage gate. Проверенные unit tests не заменяют Docker integration.
  В `internal/deploy` нет test files; container envelope/updater failure paths
  не получили отдельного live или fault-injection доказательства в этом review.

Diagnostic logs находятся только в ignored `.work/tmp/` task worktree;
в git включены выводы, не сырые логи. Общий test suite, Linux runtime,
реальный cutover/rollback, throughput/RAM и sish integration не проверялись.
Нет утверждения о production readiness или полном security audit Haloy.

## Последствие для ADR/SPEC

На момент завершения source review ADR-0081 был Proposed, SPEC-0079 Draft.
Обновление по принятому владельцем решению 2026-10-26: ADR Accepted,
SPEC Frozen; реализация выделена в CHG-0170–0174. Предыдущий открытый вопрос
«restricted Haloy adapter или расширение нашего controller» закрыт этим
design review в пользу второго. Новых dependencies/production code нет.
Следующий шаг — реализация H1–H5 по выделенным CHG; Docker остаётся
за пределами текущего поручения. Dynamic completion всё ещё требует реальных
runsc/Kata, file/network/resource canaries и sish/browser acceptance.
