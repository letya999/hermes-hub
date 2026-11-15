# Промпт: foundation M32 — только проверенная общая база

> Устарело 2026-11-05: владелец отменил отдельный foundation. Этот промпт не
> запускать. Актуальный порядок — [parallel-work.md](parallel-work.md):
> reviewed commit → dev → три ветки от одного SHA.

Подготовь foundation в репозитории C:/Users/User/a_projects/hermes-hub.
Обязательное чтение: AGENTS.md, README.md, docs/index.md и
specs/active/SPEC-0096-m32-foundation.md. Этого достаточно для задания.
Не читай все #508–#527, старые аудиты, ADR и спеки подряд.

Результат: отдельная проверенная ветка с уже существующей работой.
Не реализовывай issues M32, новые интерфейсы или перенос сервисной логики.

1. Проверь HEAD/status/worktrees. По явному allowlist перенеси существующие
   изменения и необходимые предпосылки в отдельный checkout
   C:/Users/User/a_projects/hermes-hub-wt/m32-foundation,
   ветка codex/m32-foundation. Исходные файлы/staging не меняй.
   Учитывай staged/unstaged, новые source/docs и удаления. Не копируй private
   spaces, secrets, deployments, transcripts или runtime state. Без bulk add,
   reset/stash/clean; при неоднозначном hunk назови конкретный blocker.
2. Создай локальный candidate commit. На нём выполни just check (coverage >=85%)
   и применимые gates AGENTS.md. Docker evidence должен соответствовать source SHA.
   Допустима одна локальная packaging/fixture-поправка. Более широкий дефект —
   отдельная repair-задача; не превращай foundation в реализацию milestone.
3. Запиши foundation-ready.md рядом с этим промптом: source/tested SHA,
   включённые/исключённые пути, команды, exit codes, coverage, image identity,
   BASELINE_READY=YES/NO и blockers. Pending gate не равен pass.
4. При READY опубликуй codex/m32-foundation и codex/m32-integration без force,
   не затирая чужую работу. Не merge в dev/main и не запускай implementation agents.

В ответе: READY/BLOCKED/PENDING_GATE, integration SHA, receipt и результаты gates.
Следующие отдельные назначения — #509, #513, #521. Ты их не выполняешь.

