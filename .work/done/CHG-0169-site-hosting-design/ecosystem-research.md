# Hosting ecosystem research — MVP decision for #147

Дата проверки: 2026-10-26. Продолжение [research.md](research.md).
Прочитаны публичные документы, GitHub README/архитектура и отдельные исходники;
вызваны Context7 MCP resolve + query для Dokku, Coolify, OpenSandbox.
Ничего не установлено, не запущено и не опубликовано; Docker не вызывался.
Это desk research, а не сравнительный benchmark или аудит безопасности.

## Результат для Hermes Hub

Рекомендуется сохранить собственный небольшой Go hosting boundary, но начать
с **законченного static MVP**, затем добавить **один** dynamic-профиль.
HTML/CSS/JS с клиентской интерактивностью относится к static: ему не нужен
серверный процесс на каждый сайт. Обычный subprocess без confinement отвергнут;
корректный OS sandbox технически возможен, но в проекте уже есть OCI admission.

Готовое решение для статических artifacts действительно найдено: **artifact-site**.
Если задача была бы «быстро поставить отдельный artifacts-продукт», это главный
кандидат на пробу. Для текущего встроенного #147 оно требует Next.js/Postgres,
отдельной auth-модели и адаптера ownership; dynamic backend не предоставляет.
Нельзя объявить его drop-in заменой внутреннего hosting.

Если владелец впоследствии выберет managed hosting, наиболее близкий готовый
конструктор — Cloudflare Workers for Platforms + официальные templates. Это
другой operational выбор: CF account/runtime/API, новые provider grants и
внешняя обработка кода. Он не потребляет локальные sish/DNS так, как требует
текущая ветка. В этом проходе такой выбор не принят и аккаунты не затронуты.

## 1. Готовые решения: что реально можно переиспользовать

| Решение / проверенный источник | Что уже есть | Цена интеграции / вывод для MVP |
|---|---|---|
| [artifact-site README](https://github.com/lexmount/artifact-site/blob/main/README.md), [architecture](https://github.com/lexmount/artifact-site/blob/main/ARCHITECTURE.md) | Приём готового HTML/build-folder, версии, CLI/MCP, sharing; Node/Next.js + production Postgres, local/S3 bytes | Самый близкий готовый static-продукт; нет серверных приложений. Отдельная opt-in интеграция возможна, встраивание сейчас добавляет запрещённый unsolicited DB/framework |
| [E2B Fragments](https://github.com/e2b-dev/fragments) | Open-source UX генерации приложений, несколько template runtimes, preview URL | Next.js frontend и E2B account; дублирует агентский UI. Брать образец profile→sandbox→URL, не весь продукт |
| [Dokku](https://github.com/dokku/dokku), [resource limits](https://github.com/dokku/dokku/blob/master/docs/advanced-usage/resource-management.md), [user management](https://github.com/dokku/dokku/blob/master/docs/deployment/user-management.md) | Мини-PaaS, deployment/process lifecycle, явные resource limits | Хороший отдельный backend для доверенных owner-приложений. По умолчанию ресурсы unlimited, user-specific command authorization расширяется плагинами. Наши quotas/Broker/identity/gateway всё равно нужны |
| [Coolify Compose](https://github.com/coollabsio/coolify/blob/main/docker-compose.yml), [security model](https://github.com/coollabsio/coolify-docs/blob/main/content/docs/core/security-model.mdx) | Полный PaaS control plane, team-scoped API, static/dynamic deployments; Postgres + Redis | Сильный standalone вариант, но второй привилегированный control plane рядом с hub. Team/project не доказательство OS/network isolation недоверенного приложения |
| [Dokploy](https://github.com/Dokploy/dokploy), [license](https://github.com/Dokploy/dokploy/blob/canary/LICENSE.MD) | Docker deployment platform | Альтернатива отдельному PaaS, а не маленькая библиотека. В license есть отдельная proprietary область; нельзя повторять обзорное «всё Apache» без оговорки |
| [CapRover](https://github.com/caprover/caprover), [license](https://github.com/caprover/caprover/blob/master/LICENSE) | Docker/nginx PaaS | Ещё один standalone вариант. License содержит дополнительные условия поверх Apache-текста; для embed не выбран, правовой вывод не делался |
| [OpenSandbox](https://github.com/opensandbox-group/OpenSandbox), [multi-tenancy](https://github.com/opensandbox-group/OpenSandbox/blob/main/docs/guides/multi-tenancy.md), [secure access](https://github.com/opensandbox-group/OpenSandbox/blob/main/docs/guides/secure-access.md) | API/Go SDK, sandbox lifecycle, Docker/K8s backends, ingress/egress | Самый содержательный reference для lifecycle. Но multi-tenancy требует K8s, `secureAccess` отклоняется Docker backend. Добавляет Python server и свой state/control plane; не бесплатная замена существующему controller |
| [Caddy API](https://caddyserver.com/docs/api), [file server](https://caddyserver.com/docs/quick-starts/static-files) | Готовые file server/reverse proxy, durable config reload | Может быть data plane при уже установленном Caddy. Нужны наши auth/quota/audit/ownership. Для текущего Go процесса stdlib меньше по общей системе; Caddy admin надо изолировать, auto-TLS не дублировать поверх sish |
| [workerd README](https://github.com/cloudflare/workerd/blob/main/README.md) | Готовый JS/Wasm HTTP runtime, capability bindings | Сам README предупреждает: не hardened sandbox сам по себе. Без внешней OS/VM-границы не заменяет admission; Workers API ограничивает совместимость приложений |
| [nsjail](https://github.com/google/nsjail) | Linux namespaces, seccomp, cgroup/rlimit, лимит времени | Реальная готовая OS-process альтернатива, существенно лучше raw subprocess. Но потребует нового профиля, ingress/lifecycle/receipts и native prerequisite; текущий controller уже решает часть этих задач |
| [Piku](https://github.com/piku/piku) | Маленький процессный PaaS с Procfile, nginx/uWSGI, static worker | Полезный образец минимализма. Virtualenv/раздельные dependencies сами по себе не являются security boundary против hostile code; не выбран для user code |

### Проверка примеров по коду, а не по обещанию README

- В [Fragments route на просмотренном commit](https://github.com/e2b-dev/fragments/blob/3b11c7fcd4dcc37b2f1effde35960cf6ec4bd3c7/app/api/sandbox/route.ts)
  sandbox создаётся с 10-minute timeout и URL через `getHost`. Это конкретный
  preview-flow, не доказательство durable service. Метаданные user/team приходят
  из request body — такой identity-паттерн в hub переносить нельзя.
- [artifact-site SECURITY](https://github.com/lexmount/artifact-site/blob/main/SECURITY.md)
  описывает opaque-origin iframe/CSP, постоянную проверку revoke/expiry и
  ограничение rate limiter одним replica. Там же recoverable share tokens
  названы plaintext-исключением. Это отличается от hash-only утверждения в
  architecture: чужой дизайн нельзя копировать без сверки конкретного контракта.
  Проверенный [share.ts](https://github.com/lexmount/artifact-site/blob/main/src/lib/share.ts)
  отдельно подчёркивает gate на **каждом byte-serving пути**; это полезный инвариант.
- OpenSandbox [configuration](https://github.com/opensandbox-group/OpenSandbox/blob/main/docs/getting-started/configuration.md)
  различает renewal абсолютного expiry и «добавить duration». Перенос вызова
  без разбора семантики может неожиданно укоротить lease. Hub должен хранить
  собственный site deadline и проверять его при любом renew/reconnect.

Проверка лицензий здесь — чтение метаданных/текста конкретных проектов, не
юридическая экспертиза. Ни одного стороннего исходника в hub не скопировано.

## 2. Как устроено у провайдеров

| Провайдер / источник | Проверенная модель | Что переносим / что не предполагаем |
|---|---|---|
| [Claude Artifacts](https://support.claude.com/en/articles/9487310-what-are-artifacts-and-how-do-i-use-them), [sharing](https://support.claude.com/en/articles/9547008-publish-and-share-artifacts) | Отдельный published artifact, publish/unpublish; опубликованным artifacts доступно bounded personal/shared text storage. Sharing различается между новым и предыдущим UX | Разделять draft и published revision, явно отзывать доступ. Это не свидетельство произвольного Linux backend; их storage/login модель не переносится автоматически |
| [Replit publishing](https://docs.replit.com/learn/projects-and-artifacts/replit-deployments) | Snapshot опубликованного app отделён от Project Editor; Static, Autoscale, Reserved VM, Scheduled — разные deployment types | Build workspace не production root; постоянный сервис не dev-server. Для MVP достаточно static + bounded always-running app, без собственного autoscale |
| [Netlify atomic deploys](https://docs.netlify.com/deploy/deploy-overview/), [preview URLs](https://docs.netlify.com/deploy/deploy-types/deploy-previews/) | Полный deploy активируется атомарно; mutable site URL и immutable deploy permalink различаются | Immutable snapshot + один active pointer. Не добавлять permalink/history UI в MVP: revision уже нужен для concurrency, лишние URL не нужны |
| [Vercel Sandbox](https://vercel.com/sandbox), [duration/persistence](https://vercel.com/kb/guide/vercel-sandbox-duration-and-persistence) | Firecracker boundary; running session timeout и сохранение sandbox filesystem — отдельные контроли | Не путать время процесса с published site lifetime и сроком share. Не утверждать, что sandbox теперь всегда ephemeral или что preview URL сам гарантирует постоянный hosting |
| [E2B security](https://e2b.dev/security) | Отдельная Firecracker microVM, timeout/shutdown, pause/resume состояния | Сильная isolation-модель за внешним сервисом. Цена для hub — внешний provider/API и deployment authorization; не внедряем скрытно |
| [Fly Sprites](https://docs.fly.io/sprites/working-with-sprites) | Файловая система сохраняется, RAM нет; URL умеет wake; Services перезапускаются при wake, обычная TTY не эквивалентна service; URL private по умолчанию | Нужен самостоятельный service lifecycle. Не имитировать sleep/wake/checkpoint в MVP; это отдельная сложность |
| [Cloudflare Workers for Platforms](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/how-workers-for-platforms-works/), [security model](https://developers.cloudflare.com/workers/reference/security-model/) | Доверенный dispatcher выбирает User Worker, применяет platform policy; runtime имеет дополнительные слои защиты | Хороший образец trusted gate перед user code. Самодельный `workerd` процесс не равен managed Cloudflare isolation |
| [Deno: авторский security-разбор 2023](https://denoland.medium.com/how-security-and-tenant-isolation-allows-deno-subhosting-to-run-untrusted-code-securely-355dc1c3bff0), [v1 deprecation](https://docs.deno.com/subhosting/manual/) | Описаны isolate + process/namespaces/seccomp + cgroups/watchdog. V1 API sunset 2026-07-20, документы отсылают на v2 | Важен многослойный boundary, а не просто Deno permissions. Старый API и историческую архитектуру нельзя выдавать за текущий проверенный контракт v2 |

### Действительно готовые managed templates

Официальный [Worker Publisher](https://github.com/cloudflare/templates/tree/main/worker-publisher-template)
показывает deploy в dispatch namespace и trusted router.
[Internal Static Sites](https://github.com/cloudflare/templates/tree/main/internal-sites-template)
добавляет folder/ZIP upload, subdomain, D1 metadata и Cloudflare Access.
Это самый короткий внешний путь «upload → URL», если принять CF-зависимость.

[VibeSDK](https://github.com/cloudflare/vibesdk) показывает полный AI-app workflow:
workspace, bundle, Dynamic Worker preview, per-app Durable Object storage.
Для Hermes это reference boundaries, а не предложение заменить его agent loop.
Локальные sish, Broker и namespaces пришлось бы связать с другим hosting provider.

## 3. Context7 MCP: что проверено

Вызваны три resolve и три query, в рамках лимита инструментов:

| Library ID | Вопрос | Результат и перепроверка |
|---|---|---|
| `/dokku/dokku` | Resource/process lifecycle и scope SSH users | Явные лимиты, unlimited default; user scoping перепроверен по upstream user-management.md |
| `/coollabsio/coolify-docs` | API permissions и networking security boundary | Team ownership не равно network namespace; upstream security model и Compose прочитаны отдельно |
| `/websites/open-sandbox_ai` | Docker lifecycle, TTL, ingress, quotas | Backend-dependent гарантии; K8s-only tenancy/secureAccess перепроверены в репозитории |

Context7 использован как navigation/documentation retrieval, не как proof
реального исполнения. У resolve был неоднозначный description для репозитория
OpenSandbox; выбран docs-site ID, затем найден фактический upstream
`opensandbox-group/OpenSandbox` (старый `alibaba/OpenSandbox` перенаправляется).

## 4. Поиск по запрошенным площадкам

Истории пользователей и авторские тексты помогают найти failure modes;
технические гарантии сверялись с первичными источниками выше.
Давность, рекламная заинтересованность и ограничения доступа отмечены явно.

| Площадка / что прочитано | Полезный сигнал | Ограничение |
|---|---|---|
| [Hacker News: автор QuickJS wrapper](https://news.ycombinator.com/item?id=40897658), 2024-07-07 | Автор указывает, что host-authenticated fetch в sandbox API ломает credential boundary. Добавлен запрет ambient browser/server credentials | Это first-person предупреждение об одном wrapper, не benchmark всех sandbox |
| [HN: Coolify discussion](https://news.ycombinator.com/item?id=43555996), 2025-04-04 | Есть жалобы на deploy/drain и ответы о развитии продукта. Нужны собственные replacement/drain проверки | Исторические комментарии не описывают гарантии актуальной версии |
| [Reddit: self-host E2B](https://www.reddit.com/r/LocalLLaMA/comments/1fy48b4/is_there_any_selfhostable_alternative_to_e2bs/), 2024 и поздние ответы | Название «open source» не гарантирует простой актуальный self-host recipe; источник кандидатов | Советы устаревают, есть self-promotion. Не приняли их утверждения о нынешнем статусе Daytona за факт |
| [Reddit: Coolify→Dokku](https://www.reddit.com/r/selfhosted/comments/1jex4ie/switched_from_coolify_to_dokkusame_apps_half_the/), 2025-03-19 | Обсуждают overhead/операционные проблемы панели на маленьком VPS | Не используем чужие RAM/CPU/стоимость как измерения для hub |
| [DEV comparison](https://dev.to/ameistad/self-hosted-deployment-tools-compared-coolify-dokploy-kamal-dokku-and-haloy-2npd) | Удобная карта CLI/PaaS/GUI вариантов | Обзор ошибочно упрощает implementation/license детали; отбор проверен по upstream, не по популярности |
| [Medium: Deno engineering](https://denoland.medium.com/how-security-and-tenant-isolation-allows-deno-subhosting-to-run-untrusted-code-securely-355dc1c3bff0), 2023-11-27 | Первичный рассказ о resource fairness и defense in depth | Исторический материал; старый Subhosting API уже sunset |
| [Substack: CreateOS авторы](https://nodeops.substack.com/p/createos-sandbox-for-jenkins-a-fresh), 2026-10-06 | Build code и control plane разведены; отдельный guest на build | Vendor announcement для CI, не независимое доказательство hosting. Другой материал про microVM lifecycle нашёлся, но full fetch не прошёл |
| [Habr: gVisor тренажёр](https://habr.com/ru/articles/1099036/), проверено 2026-10-26 | Автор описывает реальные несовместимости overlay/tmpfs/inodes. В план внесены runtime-specific disk/PID/tmpfs canaries | Не переносим его привилегии/flags и не обобщаем единичный опыт на все версии runsc |
| X: targeted search E2B/Fragments, Fly/Sprites; [профиль E2B](https://x.com/e2b_dev) | Прямой fetch дал 403, поиск преимущественно нерелевантен; зеркала найдены | Никакого подтверждённого технического вывода из X. Зеркала/сниппеты не выданы за прочитанные оригинальные посты |
| arXiv: [Sandlock](https://arxiv.org/abs/2605.26298), 2026-05-25 | Реальная альтернатива containers: kernel policies + небольшой supervisor | Исследовательские результаты автора; нет доказательства готового quota/durable hosting contract для нашего deployment |
| Archive.org: поиск Firecracker / запрос каталога | Релевантного проверяемого первичного материала не получено; каталог недоступен через web tool | Архив не использован как evidence. «arhieve» проверено и как Archive.org, и как возможное arXiv |

Дополнительно прочитан первичный [Firecracker NSDI 2020](https://www.usenix.org/conference/nsdi20/presentation/agache):
microVM — реальный способ совместить multitenancy и сильную границу, но VMM сам
не предоставляет наш publisher, shares, quota ledger или revision fencing.

## 5. Что меняется в Proposed-дизайне после research

1. **MVP-S: сначала законченная статика.** Готовый folder/HTML → immutable
   snapshot → авторизованный share URL; list/unpublish, quota, audit,
   endpoint reconciliation переживают конец чата. Никакого server execution
   для клиентского JS. Все связи с exposure/stack/tools входят в эту поставку.
2. **MVP-D: один stateless HTTP profile.** Предлагается `python-http` с
   фиксированным stdlib runtime/entrypoint convention в отдельном pinned image.
   Никакого runtime `pip/npm install`, dev-server/hot reload, Dockerfile или
   произвольного image. Произвольная application logic остаётся user code.
3. **Явный isolation tier.** Reference для dynamic — `runsc` (gVisor),
   `kata` как проверяемая альтернатива. Оба имени уже поддержаны
   `internal/toolhub/cell.go:runtimeFlag`; по умолчанию существующий CLI
   умеет runc, поэтому простой reuse без hosting policy недостаточен.
   Динамический publish без выбранного secure runtime/receipt/canaries
   отказывает. Это Proposed prerequisite, не установка в текущем проходе.
4. **Build — отдельная execution boundary.** Hosting принимает только
   законченные bytes. Не исполняет install/build hooks в Hermes или publisher.
   Если нужен исполняемый build, используем существующий admitted build/CLI
   путь с отдельными limits/egress, без inherited secrets. Нет такого пути —
   возвращаем unsupported build, а не запускаем код рядом с кредами.
5. **Один writer/replica на user/env.** CLI и агент идут в control API;
   файловый store и existing lock/revision patterns, без внешних DB/Redis и
   распределённых счётчиков. Второй writer отказывает. Cross-process quota
   race tests всё равно нужны для lock/recovery.
6. **Browser budget — отдельно от server sandbox.** Ограничить browser CSP,
   запретить service workers по умолчанию (`worker-src 'none'`), ambient hub
   credentials и произвольные connect targets. Не объявлять downloaded bytes
   отзываемыми: revoke останавливает новые server reads, не удаляет копии.
   Opaque-origin iframe не навязывается обычному multi-file сайту без проверки
   module/assets/cookie совместимости; browser canary обязателен.
7. **Отложить необязательное.** Visitors OIDC, dashboards/comments/remix,
   distributed replicas, HA, autoscale/sleep/snapshots, writable app volumes,
   dependency installer, multiple runtime languages и recoverable raw tokens
   не нужны первому MVP. Explicit public и bearer-link semantics сохраняются.

MVP-S — полезная первая поставка, но **не закрывает весь #147**. Завершение
issue требует MVP-D и доказательств реального Linux/runtime/sish. Secret-env
расширение — только через Broker и отдельную явную авторизацию; unsupported
запрос до его реализации fail-closed. Цель сервиса после чата сохранена,
микроVM fleet и новый PaaS control plane не добавлены.

## 6. Evidence gaps и следующий шаг

### Уточнение: Go-native платформы не отсутствуют

После вопроса владельца отдельно проверены Go-кандидаты; прежний обзор
недостаточно явно выделял этот срез. Язык реализации не является причиной
отказа от готового backend.

| Кандидат | Проверенный состав | Вывод |
|---|---|---|
| [Haloy](https://haloy.dev/docs/what-is-haloy), [go.mod](https://github.com/haloydev/haloy/blob/main/go.mod) | Go CLI + haloyd + отдельный haloy-proxy; Docker deployment/health/routing; SQLite dependency вместо обязательного внешнего Postgres/Redis | Наиболее близкий лёгкий lifecycle-кандидат. Не доказано, что адаптер дороже собственного lifecycle. Требуется отдельная проверка соответствия до реализации |
| [SwiftWave](https://swiftwave.org/), [repository](https://github.com/swiftwave-org/swiftwave) | Go PaaS; официальный состав включает Postgres/HAProxy; собственный deployment/routing stack | Готовая standalone платформа, не просто библиотека. Не отбрасывать из-за Go; учитывать смену control/data plane |
| [Orbita architecture](https://github.com/MUKE-coder/orbita#architecture), [go.mod](https://github.com/MUKE-coder/orbita/blob/main/go.mod) | Go binary с React UI, Postgres, Redis, Swarm, Traefik | Single binary не означает отсутствие инфраструктурных зависимостей. README isolation claims не проверены исполнением |
| [Tsuru](https://github.com/tsuru/tsuru) | Go PaaS, documented Kubernetes setup | Готовая полноценная платформа; слишком большая operational смена для существующего single-host MVP |

На этапе первоначального обзора для Haloy оставались конкретные вопросы: можно ли использовать lifecycle/API
за нашим sish без второго обязательного TLS/router; как обеспечить обязательные
runsc/Kata, mount/network/receipt проверки и resource limits; можно ли оградить
привилегированный API нашим identity/Broker boundary и не хранить секреты вторично.
[Configuration reference](https://haloy.dev/docs/configuration-reference)
описывает произвольные mounts/env и local CLI hooks — модель для доверенного
оператора, её нельзя напрямую отдавать агенту. Это не доказательство невозможности
безопасного адаптера. Никакого Haloy-кода не импортировано и сервис не запускался.

Первоначальная рекомендация для сравнения перед кодом: считать собственный hosting contract
обязательной частью (slots, auth, shares, quota, audit), а lifecycle backend
сравнить как **существующий controller + небольшое расширение** против
**Haloy за ограниченным адаптером**. Пока нет доказательства, что второй путь
легче или что он непригоден. Caddy остаётся готовым Go data-plane кандидатом;
gVisor/runsc — готовым isolation-компонентом, а не новой реализацией sandbox.

**Follow-up завершён:** [детальный Haloy source review](haloy-review.md)
закреплён на `3cd1d500394a81532970dc4cdb57e02d77b2be62` (локальный clone,
critical paths, selected unit/race tests, static vulnerability scan).
Предыдущая неопределённость снята: обязательный pre-start envelope нельзя
выразить неизменённым API, private-network discovery и backend HTTP за sish
требуют внутренних изменений, durable revision/tombstone/share contract
остаётся нашим. Рекомендуется расширить существующий controller/ToolHub,
не брать Haloy целиком, не поддерживать lifecycle fork. Переносим patterns
snapshot/hash/reconcile/readiness; готовый код пока не извлекаем.
Таблица выше сохраняет первоначальную оценку кандидата; актуальное решение,
точные основания и ограничения проверки находятся в haloy-review.md.

Не измерены расход памяти, startup latency, пропускная способность и стоимость
на deployment-хосте; чужие цифры не превращены в обещания для hub.
Не проверены runsc/Kata на текущем executor и их tmpfs/inode/network поведение.
Не доказаны sish hostname claims/Host preservation, browser share handshake,
или живой publish/unpublish. Новые proposed решения остаются в ADR-0079,
на момент исследования SPEC-0077 была Draft. Обновление 2026-10-26: владелец
принял направление, ADR-0079 Accepted, SPEC-0077 Frozen; implementation
CHG-0170–0174 выделены с отдельными plan/state. Исполнение и live evidence
не становятся завершёнными от принятия дизайна.
